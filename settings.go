package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// settings is a small persistent key/value store, the Windows stand-in for the
// macOS original's UserDefaults. Keys reuse the original's names where they mean
// the same thing, so the two ports stay comparable.
//
// It lives in %APPDATA%\Windline\settings.ini as `key=value` lines. Values that
// are lists are JSON-encoded on one line, which keeps the file readable and the
// parsing trivial (and dependency-free).
type settings struct {
	mu   sync.Mutex
	path string
	vals map[string]string
}

func newSettings() *settings {
	dir := appDataDir()
	s := &settings{path: filepath.Join(dir, "settings.ini"), vals: map[string]string{}}
	if data, err := os.ReadFile(s.path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if k, v, ok := strings.Cut(line, "="); ok {
				s.vals[strings.TrimSpace(k)] = v
			}
		}
	}
	return s
}

// appDataDir is %APPDATA%\Windline, created on demand.
func appDataDir() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "AppData", "Roaming")
	}
	dir := filepath.Join(base, "Windline")
	os.MkdirAll(dir, 0o755)
	return dir
}

// localDataDir is %LOCALAPPDATA%\Windline. Screenshots live here: they are big
// and machine-specific, so they do not belong in a roaming profile.
func localDataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "AppData", "Local")
	}
	dir := filepath.Join(base, "Windline")
	os.MkdirAll(dir, 0o755)
	return dir
}

func (s *settings) get(key, def string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.vals[key]; ok {
		return v
	}
	return def
}

// set writes through immediately, so a crash or a kill cannot lose a setting.
func (s *settings) set(key, value string) {
	s.mu.Lock()
	if s.vals[key] == value {
		s.mu.Unlock()
		return
	}
	s.vals[key] = value
	var b strings.Builder
	b.WriteString("# Windline settings. Written by the app.\n")
	for _, k := range sortedKeys(s.vals) {
		b.WriteString(k + "=" + s.vals[k] + "\n")
	}
	snapshot := b.String()
	s.mu.Unlock()
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(snapshot), 0o644); err == nil {
		os.Rename(tmp, s.path)
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func (s *settings) getBool(key string, def bool) bool {
	v := s.get(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func (s *settings) setBool(key string, v bool) { s.set(key, strconv.FormatBool(v)) }

func (s *settings) getInt(key string, def int) int {
	v := s.get(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func (s *settings) setInt(key string, v int) { s.set(key, strconv.Itoa(v)) }

func (s *settings) getList(key string) []string {
	v := s.get(key, "")
	if v == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil
	}
	return out
}

func (s *settings) setList(key string, list []string) {
	data, err := json.Marshal(list)
	if err != nil {
		return
	}
	s.set(key, string(data))
}

// save flushes the file. Every set already writes through, so this exists for
// the callers that want to be explicit about a batch of changes being final.
func (s *settings) save() {
	s.mu.Lock()
	keys := sortedKeys(s.vals)
	vals := make(map[string]string, len(s.vals))
	for k, v := range s.vals {
		vals[k] = v
	}
	s.mu.Unlock()

	var b strings.Builder
	b.WriteString("# Windline settings. Written by the app.\n")
	for _, k := range keys {
		b.WriteString(k + "=" + vals[k] + "\n")
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err == nil {
		os.Rename(tmp, s.path)
	}
}

// ---------------------------------------------------------------- typed state
//
// These wrap the raw keys so the rest of the app never spells a key itself, and
// so each setting has exactly one home.

// The keys, matching the macOS original where the meaning matches.
const (
	keyLineSize         = "lineSize"
	keyKeepOnLine       = "keepOnLine"
	keySoundOff         = "soundOff"
	keyShortcut         = "shortcut"
	keyInboxEnabled     = "inboxEnabled"
	keyInboxOffered     = "inboxOffered"
	keyInboxSaved       = "inboxSavedSettings"
	keyInboxRaw         = "inboxSavedRawValue"
	keyLineHidden       = "lineHidden"
	keyClipboardEnabled = "clipboardEnabled"
	keyExtraFolders     = "extraFolders"
	keyOpenFromTop      = "openFromTopEdge"
	keyPeekAtNew        = "peekAtNew"
	keyShowOverFull     = "showOverFullScreen"
	keyLineItems        = "pegged"
	keyWelcomed         = "welcomed"
	keyHideTip          = "hideTip"
	keyMenuInstructions = "menuInstructionsOff"
	keyShortcutMods     = "shortcutModifiers"
	keyShortcutKey      = "shortcutKey"
	keyWatchingFolders  = "watchedFolders"
)

// keepChoices is the Keep on line menu, ported unchanged.
var keepChoices = []int{25, 50, 100}

// keepOnLine returns how many photos the line keeps, or 0 for "as many as fit".
func (s *settings) keepOnLine() int {
	n := s.getInt(keyKeepOnLine, 0)
	for _, c := range keepChoices {
		if c == n {
			return n
		}
	}
	return 0
}

func (s *settings) setKeepOnLine(n int) {
	if n == 0 {
		s.set(keyKeepOnLine, "")
		return
	}
	s.setInt(keyKeepOnLine, n)
}

func (s *settings) lineSize() Size     { return sizeFromKey(s.get(keyLineSize, "medium")) }
func (s *settings) setLineSize(v Size) { s.set(keyLineSize, v.Key()) }

// soundOn is the inverse of the stored flag, as in the original.
func (s *settings) soundOn() bool      { return !s.getBool(keySoundOff, false) }
func (s *settings) setSoundOn(on bool) { s.setBool(keySoundOff, !on) }

func (s *settings) opensFromTop() bool     { return s.getBool(keyOpenFromTop, true) }
func (s *settings) setOpensFromTop(v bool) { s.setBool(keyOpenFromTop, v) }

func (s *settings) peeksAtNew() bool     { return s.getBool(keyPeekAtNew, true) }
func (s *settings) setPeeksAtNew(v bool) { s.setBool(keyPeekAtNew, v) }

func (s *settings) showOverFullScreen() bool     { return s.getBool(keyShowOverFull, false) }
func (s *settings) setShowOverFullScreen(v bool) { s.setBool(keyShowOverFull, v) }

func (s *settings) clipboardEnabled() bool     { return s.getBool(keyClipboardEnabled, false) }
func (s *settings) setClipboardEnabled(v bool) { s.setBool(keyClipboardEnabled, v) }

func (s *settings) inboxEnabled() bool     { return s.getBool(keyInboxEnabled, false) }
func (s *settings) setInboxEnabled(v bool) { s.setBool(keyInboxEnabled, v) }

func (s *settings) inboxOffered() bool     { return s.getBool(keyInboxOffered, false) }
func (s *settings) setInboxOffered(v bool) { s.setBool(keyInboxOffered, v) }

func (s *settings) extraFolders() []string     { return s.getList(keyExtraFolders) }
func (s *settings) setExtraFolders(v []string) { s.setList(keyExtraFolders, v) }

// lineHidden is "Hide the line": the line stays out of the way until it is asked
// for. It is separate from the reveal state, which is transient.
func (s *settings) lineHidden() bool     { return s.getBool(keyLineHidden, false) }
func (s *settings) setLineHidden(v bool) { s.setBool(keyLineHidden, v) }
