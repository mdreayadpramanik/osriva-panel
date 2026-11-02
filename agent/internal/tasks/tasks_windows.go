//go:build windows

package tasks

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

func winState(s svc.State) (string, string) {
	switch s {
	case svc.Running:
		return "running", "RUNNING"
	case svc.Stopped:
		return "stopped", "STOPPED"
	case svc.StartPending:
		return "starting", "START_PENDING"
	case svc.StopPending:
		return "stopping", "STOP_PENDING"
	case svc.Paused:
		return "stopped", "PAUSED"
	case svc.PausePending, svc.ContinuePending:
		return "unknown", "PAUSE_OR_CONTINUE_PENDING"
	}
	return "unknown", fmt.Sprint(uint32(s))
}

func winStartup(t uint32) string {
	switch t {
	case mgr.StartAutomatic:
		return "enabled"
	case mgr.StartManual:
		return "manual"
	case mgr.StartDisabled:
		return "disabled"
	case windows.SERVICE_BOOT_START, windows.SERVICE_SYSTEM_START:
		return "static"
	}
	return "unknown"
}

// openService opens a service with only the access rights needed.
func openService(m *mgr.Mgr, name string, access uint32) (*mgr.Service, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.OpenService(m.Handle, p, access)
	if err != nil {
		return nil, err
	}
	return &mgr.Service{Name: name, Handle: h}, nil
}

func listServices(ctx context.Context) (any, error) {
	m, err := connectSCM(windows.SC_MANAGER_CONNECT | windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return nil, err
	}
	defer m.Disconnect()
	names, err := m.ListServices()
	if err != nil {
		return nil, err
	}
	out := make([]agenttask.Service, 0, len(names))
	for _, n := range names {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s, err := openService(m, n, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
		if err != nil {
			continue
		}
		item := agenttask.Service{Name: n, State: "unknown", Startup: "unknown"}
		if st, err := s.Query(); err == nil {
			item.State, item.Detail = winState(st.State)
		}
		if cfg, err := s.Config(); err == nil {
			item.Description, item.Startup = cfg.DisplayName, winStartup(cfg.StartType)
		}
		s.Close()
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return agenttask.ServicesResult{Manager: "windows-scm", Services: out}, nil
}

func waitState(ctx context.Context, s *mgr.Service, want svc.State) error {
	for {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("service did not reach the expected state in time")
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func serviceAction(ctx context.Context, name, action string) (any, error) {
	m, err := connectSCM(windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	defer m.Disconnect()
	s, err := openService(m, name, windows.SERVICE_START|windows.SERVICE_STOP|windows.SERVICE_QUERY_STATUS|
		windows.SERVICE_QUERY_CONFIG|windows.SERVICE_CHANGE_CONFIG)
	if err != nil {
		return nil, fmt.Errorf("open service %s: %w", name, err)
	}
	defer s.Close()

	stop := func() error {
		if st, err := s.Query(); err == nil && st.State == svc.Stopped {
			return nil
		}
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		return waitState(ctx, s, svc.Stopped)
	}
	start := func() error {
		if st, err := s.Query(); err == nil && st.State == svc.Running {
			return nil
		}
		if err := s.Start(); err != nil {
			return fmt.Errorf("start: %w", err)
		}
		return waitState(ctx, s, svc.Running)
	}
	setStartup := func(t uint32) error {
		cfg, err := s.Config()
		if err != nil {
			return err
		}
		cfg.StartType = t
		return s.UpdateConfig(cfg)
	}

	switch action {
	case agenttask.ActionStart:
		err = start()
	case agenttask.ActionStop:
		err = stop()
	case agenttask.ActionRestart:
		if err = stop(); err == nil {
			err = start()
		}
	case agenttask.ActionEnable:
		err = setStartup(mgr.StartAutomatic)
	case agenttask.ActionDisable:
		err = setStartup(mgr.StartDisabled)
	}
	if err != nil {
		return nil, err
	}
	res := agenttask.ServiceActionResult{Name: name, Action: action, State: "unknown", Startup: "unknown"}
	if st, err := s.Query(); err == nil {
		res.State, _ = winState(st.State)
	}
	if cfg, err := s.Config(); err == nil {
		res.Startup = winStartup(cfg.StartType)
	}
	return res, nil
}

var (
	psapi                    = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo = psapi.NewProc("GetProcessMemoryInfo")
)

type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func listProcesses(ctx context.Context) (any, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	res := agenttask.ProcessesResult{Processes: []agenttask.Process{}}
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p := agenttask.Process{PID: int(pe.ProcessID), PPID: int(pe.ParentProcessID), Threads: int(pe.Threads),
			Name: windows.UTF16ToString(pe.ExeFile[:])}
		if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pe.ProcessID); err == nil {
			var mc processMemoryCounters
			mc.CB = uint32(unsafe.Sizeof(mc))
			if ok, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&mc)), uintptr(mc.CB)); ok != 0 {
				p.RSSBytes = uint64(mc.WorkingSetSize)
			}
			buf := make([]uint16, windows.MAX_PATH)
			n := uint32(len(buf))
			if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
				p.Command = windows.UTF16ToString(buf[:n])
			}
			windows.CloseHandle(h)
		}
		res.Processes = append(res.Processes, p)
	}
	sort.Slice(res.Processes, func(i, j int) bool { return res.Processes[i].RSSBytes > res.Processes[j].RSSBytes })
	if len(res.Processes) > agenttask.MaxProcesses {
		res.Processes, res.Truncated = res.Processes[:agenttask.MaxProcesses], true
	}
	return res, nil
}

// connectSCM opens the Service Control Manager with only the rights needed
// (mgr.Connect asks for full access, which only administrators get).
func connectSCM(access uint32) (*mgr.Mgr, error) {
	h, err := windows.OpenSCManager(nil, nil, access)
	if err != nil {
		return nil, fmt.Errorf("connect to the Service Control Manager: %w", err)
	}
	return &mgr.Mgr{Handle: h}, nil
}

func readLogs(ctx context.Context, p agenttask.LogsParams) (any, error) {
	logName := "System"
	switch p.Source {
	case agenttask.LogSourceApplication:
		logName = "Application"
	case agenttask.LogSourceService:
		return nil, fmt.Errorf("%w: per-service logs are not separated on Windows; use source=system or application", ErrUnsupported)
	}
	args := []string{"qe", logName, "/c:" + strconv.Itoa(p.Lines), "/rd:true", "/f:RenderedXml"}
	if q := winLevelQuery(p.MinLevel); q != "" {
		args = append(args, "/q:"+q)
	}
	out, err := run(ctx, "wevtutil", args...)
	if err != nil {
		return nil, err
	}
	return agenttask.LogsResult{Backend: "windows-eventlog", Entries: ParseWinEvents(out)}, nil
}
