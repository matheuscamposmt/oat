package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeRun records pactl calls. out and fail match on a prefix of the arguments.
type fakeRun struct {
	mu    sync.Mutex
	calls []string
	out   map[string]string
	fail  map[string]error
}

func (f *fakeRun) run(ctx context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	for prefix, err := range f.fail {
		if strings.HasPrefix(key, prefix) {
			return "", err
		}
	}
	for prefix, out := range f.out {
		if strings.HasPrefix(key, prefix) {
			return out, nil
		}
	}
	return "", nil
}

func (f *fakeRun) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

const sinksFixture = `Sink #55
	State: SUSPENDED
	Name: alsa_output.pci-0000_00_1f.3-platform-skl_hda_dsp_generic.HiFi__hw_sofhdadsp__sink
	Description: sof-hda-dsp Speaker + Headphones
	Properties:
		device.bus = "pci"
	Ports:
		[Out] Speaker: Speaker (type: Speaker, priority: 100, availability unknown)
		[Out] Headphones: Headphones (type: Headphones, priority: 200, availability group: Headphone, not available)
	Active Port: [Out] Speaker
Sink #60
	Name: bluez_output.AA_BB_CC_DD_EE_FF.1
	Properties:
		device.bus = "bluetooth"
		device.form_factor = "headset"
Sink #61
	Name: alsa_output.usb-headphones
	Properties:
		device.bus = "usb"
	Active Port: [Out] Headphones
Sink #62
	Name: alsa_output.hdmi
	Active Port: [Out] HDMI1
`

func TestParseSinksAndHeadphones(t *testing.T) {
	sinks := ParseSinks(sinksFixture)
	if len(sinks) != 4 {
		t.Fatalf("got %d sinks", len(sinks))
	}
	want := map[string]bool{
		"alsa_output.pci-0000_00_1f.3-platform-skl_hda_dsp_generic.HiFi__hw_sofhdadsp__sink": false,
		"bluez_output.AA_BB_CC_DD_EE_FF.1":                                                   true,
		"alsa_output.usb-headphones":                                                         true,
		"alsa_output.hdmi":                                                                   false,
	}
	for _, s := range sinks {
		if s.IsHeadphones() != want[s.Name] {
			t.Errorf("%s: IsHeadphones = %v", s.Name, s.IsHeadphones())
		}
	}
	if sinks[0].ActivePort != "[Out] Speaker" || sinks[1].Bus != "bluetooth" || sinks[1].FormFactor != "headset" {
		t.Fatalf("fields: %+v", sinks[:2])
	}
}

func TestWantEcho(t *testing.T) {
	speaker := SinkInfo{ActivePort: "[Out] Speaker"}
	phones := SinkInfo{ActivePort: "[Out] Headphones"}
	if !WantEcho("auto", speaker) || WantEcho("auto", phones) || !WantEcho("on", phones) || WantEcho("off", speaker) {
		t.Fatal("wrong decision")
	}
}

func TestPactlCommands(t *testing.T) {
	f := &fakeRun{out: map[string]string{
		"load-module":        "536870913\n",
		"get-default-sink":   "alsa_output.speaker\n",
		"list modules short": "536870913\tmodule-echo-cancel\taec_method=webrtc sink_name=oat_ec_sink\t\n12\tmodule-null-sink\t\n",
		"list sinks":         sinksFixture,
	}}
	p := Pactl{Run: f.run}
	ctx := context.Background()
	idx, err := p.LoadEchoCancel(ctx, "mic", "speaker")
	if err != nil || idx != 536870913 {
		t.Fatalf("LoadEchoCancel = %d %v", idx, err)
	}
	wantArgs := "load-module module-echo-cancel aec_method=webrtc source_master=mic sink_master=speaker source_name=oat_ec_source sink_name=oat_ec_sink"
	if f.calls[0] != wantArgs {
		t.Fatalf("args = %q", f.calls[0])
	}
	if s, _ := p.DefaultSink(ctx); s != "alsa_output.speaker" {
		t.Fatalf("DefaultSink = %q", s)
	}
	name, args, ok, err := p.Module(ctx, 536870913)
	if err != nil || !ok || name != "module-echo-cancel" || !strings.Contains(args, "sink_name=oat_ec_sink") {
		t.Fatalf("Module = %q %q %v %v", name, args, ok, err)
	}
	if _, _, ok, _ := p.Module(ctx, 99); ok {
		t.Fatal("found a module that does not exist")
	}
	if s, err := p.Sink(ctx, "alsa_output.hdmi"); err != nil || s.ActivePort != "[Out] HDMI1" {
		t.Fatalf("Sink = %+v %v", s, err)
	}
}

func TestExecUsesCLocale(t *testing.T) {
	out, err := Exec(context.Background(), "sh", "-c", "echo $LC_ALL")
	if err != nil || strings.TrimSpace(out) != "C" {
		t.Fatalf("got %q %v", out, err)
	}
	if _, err := Exec(context.Background(), "sh", "-c", "echo boom >&2; exit 3"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
}

func newEcho(t *testing.T, f *fakeRun) *Echo {
	return &Echo{Pactl: Pactl{Run: f.run}, StatePath: filepath.Join(t.TempDir(), "state", "echo.json")}
}

func TestEchoEnableDisable(t *testing.T) {
	f := &fakeRun{out: map[string]string{"load-module": "7\n"}}
	e := newEcho(t, f)
	ctx := context.Background()
	if err := e.Enable(ctx, "mic", "speaker"); err != nil {
		t.Fatal(err)
	}
	if !e.Active() || !f.called("set-default-sink oat_ec_sink") {
		t.Fatalf("calls %v", f.calls)
	}
	st, err := readState(e.StatePath)
	if err != nil || st.Module != 7 || st.PrevSink != "speaker" || st.PID != os.Getpid() {
		t.Fatalf("state %+v %v", st, err)
	}
	if err := e.Disable(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !f.called("set-default-sink speaker") || !f.called("unload-module 7") || e.Active() {
		t.Fatalf("calls %v", f.calls)
	}
	if _, err := os.Stat(e.StatePath); !os.IsNotExist(err) {
		t.Fatal("the state file still exists")
	}
}

func TestEchoDisableWithoutRestore(t *testing.T) {
	f := &fakeRun{out: map[string]string{"load-module": "7\n"}}
	e := newEcho(t, f)
	e.Enable(context.Background(), "mic", "speaker")
	e.Disable(context.Background(), false)
	if f.called("set-default-sink speaker") || !f.called("unload-module 7") {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestEchoEnableFailureCleansUp(t *testing.T) {
	f := &fakeRun{out: map[string]string{"load-module": "7\n"}, fail: map[string]error{"set-default-sink": errors.New("no sink")}}
	e := newEcho(t, f)
	if err := e.Enable(context.Background(), "mic", "speaker"); err == nil {
		t.Fatal("want an error")
	}
	if e.Active() || !f.called("unload-module 7") {
		t.Fatalf("calls %v", f.calls)
	}
	if _, err := os.Stat(e.StatePath); !os.IsNotExist(err) {
		t.Fatal("the state file still exists")
	}
}

func TestCleanupStale(t *testing.T) {
	modules := "7\tmodule-echo-cancel\taec_method=webrtc source_name=oat_ec_source sink_name=oat_ec_sink\t\n"
	cases := []struct {
		name        string
		pid         int
		modules     string
		wantRemoved bool
	}{
		{"dead process", 999999999, modules, true},
		{"live process", os.Getppid(), modules, false},
		{"module gone", 999999999, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRun{out: map[string]string{"list modules short": tc.modules, "get-default-sink": ECSink + "\n"}}
			e := newEcho(t, f)
			writeState(e.StatePath, &echoState{Module: 7, PrevSink: "speaker", PID: tc.pid})
			removed, err := e.CleanupStale(context.Background())
			if err != nil || removed != tc.wantRemoved {
				t.Fatalf("removed = %v, err = %v", removed, err)
			}
			if tc.wantRemoved && (!f.called("set-default-sink speaker") || !f.called("unload-module 7")) {
				t.Fatalf("calls %v", f.calls)
			}
			if tc.name == "live process" {
				if len(f.calls) != 0 {
					t.Fatalf("touched a live module: %v", f.calls)
				}
				if _, err := os.Stat(e.StatePath); err != nil {
					t.Fatal("removed the state file of a live process")
				}
			}
		})
	}
}

func TestReadFrames(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < 2*FrameSamples; i++ {
		binary.Write(&buf, binary.LittleEndian, int16(i-5))
	}
	buf.Write([]byte{1, 2, 3}) // partial frame
	out := make(chan []int16, 4)
	ReadFrames(&buf, out)
	close(out)
	var frames [][]int16
	for f := range out {
		frames = append(frames, f)
	}
	if len(frames) != 2 || frames[0][0] != -5 || frames[1][FrameSamples-1] != int16(2*FrameSamples-6) {
		t.Fatalf("got %d frames", len(frames))
	}
}

func fakeParec(t *testing.T, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "parec")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := ParecPath
	ParecPath = path
	t.Cleanup(func() { ParecPath = old })
}

func TestStreamGivesFrames(t *testing.T) {
	fakeParec(t, "head -c "+strconv.Itoa(10*FrameSamples*2)+" /dev/zero")
	s, err := Open(context.Background(), "mic")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for range s.Frames() {
		n++
	}
	if n != 10 || s.Err() != nil {
		t.Fatalf("frames = %d, err = %v", n, s.Err())
	}
}

func TestStreamReportsParecFailure(t *testing.T) {
	fakeParec(t, "echo 'Stream error: No such entity' >&2; exit 1")
	s, err := Open(context.Background(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	for range s.Frames() {
	}
	if s.Err() == nil || !strings.Contains(s.Err().Error(), "No such entity") {
		t.Fatalf("Err = %v", s.Err())
	}
}

func TestStreamClose(t *testing.T) {
	fakeParec(t, "exec cat /dev/zero")
	s, err := Open(context.Background(), "mic")
	if err != nil {
		t.Fatal(err)
	}
	<-s.Frames()
	s.Close()
	if s.Err() != nil {
		t.Fatalf("Err after Close = %v", s.Err())
	}
}
