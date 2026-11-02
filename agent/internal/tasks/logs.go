package tasks

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"strconv"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/pkg/agenttask"
)

// maxMessage bounds one log message so a single huge line cannot fill the result.
const maxMessage = 4000

func clip(s string) string {
	s = strings.TrimRight(s, "\r\n")
	if len(s) > maxMessage {
		return s[:maxMessage] + "…"
	}
	return s
}

// journalLevel maps syslog priorities (0 emerg … 7 debug).
func journalLevel(p string) string {
	switch p {
	case "0", "1", "2", "3":
		return "error"
	case "4":
		return "warning"
	case "5", "6":
		return "info"
	case "7":
		return "debug"
	}
	return "info"
}

// journalPriority maps a minimum level to journalctl -p.
func journalPriority(min string) string {
	switch min {
	case "error":
		return "err"
	case "warning":
		return "warning"
	case "debug":
		return "debug"
	}
	return "info"
}

type journalEntry struct {
	Realtime   string          `json:"__REALTIME_TIMESTAMP"`
	Priority   string          `json:"PRIORITY"`
	Unit       string          `json:"_SYSTEMD_UNIT"`
	Identifier string          `json:"SYSLOG_IDENTIFIER"`
	Message    json.RawMessage `json:"MESSAGE"`
}

// journalMessage decodes MESSAGE, which journald emits as an array of bytes when it is not valid UTF-8.
func journalMessage(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b []byte
	var nums []int
	if json.Unmarshal(raw, &nums) == nil {
		for _, n := range nums {
			b = append(b, byte(n))
		}
		return strings.ToValidUTF8(string(b), "�")
	}
	return ""
}

// ParseJournal parses `journalctl --output=json` (one object per line).
func ParseJournal(out []byte) []agenttask.LogEntry {
	entries := []agenttask.LogEntry{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var e journalEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		ts := ""
		if us, err := strconv.ParseInt(e.Realtime, 10, 64); err == nil {
			ts = time.UnixMicro(us).UTC().Format(time.RFC3339)
		}
		src := e.Unit
		if src == "" {
			src = e.Identifier
		}
		entries = append(entries, agenttask.LogEntry{TS: ts, Level: journalLevel(e.Priority), Source: src, Message: clip(journalMessage(e.Message))})
	}
	return entries
}

type winEvent struct {
	System struct {
		Provider struct {
			Name string `xml:"Name,attr"`
		} `xml:"Provider"`
		Level       int `xml:"Level"`
		TimeCreated struct {
			SystemTime string `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
	} `xml:"System"`
	Rendering struct {
		Message string `xml:"Message"`
	} `xml:"RenderingInfo"`
}

func winLevel(l int) string {
	switch l {
	case 1, 2:
		return "error"
	case 3:
		return "warning"
	case 5:
		return "debug"
	}
	return "info" // 0 (LogAlways) and 4
}

// winLevelQuery returns an XPath filter for a minimum level ("" = none).
func winLevelQuery(min string) string {
	switch min {
	case "error":
		return "*[System[(Level=1 or Level=2)]]"
	case "warning":
		return "*[System[(Level=1 or Level=2 or Level=3)]]"
	case "info", "":
		return "*[System[(Level=0 or Level=1 or Level=2 or Level=3 or Level=4)]]"
	}
	return ""
}

// ParseWinEvents parses concatenated `wevtutil qe … /f:RenderedXml` events
// (newest first) and returns them oldest first.
func ParseWinEvents(out []byte) []agenttask.LogEntry {
	dec := xml.NewDecoder(bytes.NewReader(out))
	var entries []agenttask.LogEntry
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Event" {
			continue
		}
		var ev winEvent
		if dec.DecodeElement(&ev, &se) != nil {
			continue
		}
		ts := ev.System.TimeCreated.SystemTime
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			ts = t.UTC().Format(time.RFC3339)
		}
		entries = append(entries, agenttask.LogEntry{TS: ts, Level: winLevel(ev.System.Level), Source: ev.System.Provider.Name,
			Message: clip(strings.TrimSpace(ev.Rendering.Message))})
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	if entries == nil {
		entries = []agenttask.LogEntry{}
	}
	return entries
}
