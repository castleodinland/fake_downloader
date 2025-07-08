package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"fake_dowloader/util"
)

//go:embed templates/*
var templateFS embed.FS

//go:embed py/force_reannounce.py
var pythonScript []byte

var (
	templates *template.Template
	devMode   bool
)

// --- Session Management ---
// Session represents a user's connection session.
type Session struct {
	ID        string
	StopChan  chan struct{}
	Speed     *int64
	IsRunning bool
	PeerAddr  string // Store peer address for status recovery
	InfoHash  string // Store info hash for status recovery
	mutex     sync.RWMutex
}

// SetRunning sets the running state of the session.
func (s *Session) SetRunning(running bool, peerAddr, infoHash string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.IsRunning = running
	if running {
		s.PeerAddr = peerAddr
		s.InfoHash = infoHash
	}
}

// GetStatus gets the current status of the session.
func (s *Session) GetStatus() (bool, string, string) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.IsRunning, s.PeerAddr, s.InfoHash
}

// SessionManager manages active sessions.
type SessionManager struct {
	sessions map[string]*Session
	mutex    sync.RWMutex
}

// NewSessionManager creates a new session manager.
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
	}
}

// GetOrCreateSession retrieves a session by ID, or creates it if it doesn't exist.
func (sm *SessionManager) GetOrCreateSession(sessionID string) *Session {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if session, exists := sm.sessions[sessionID]; exists {
		return session
	}

	session := &Session{
		ID:       sessionID,
		StopChan: make(chan struct{}),
		Speed:    new(int64),
	}
	sm.sessions[sessionID] = session
	return session
}

// StopSession stops a running session and resets its state.
func (sm *SessionManager) StopSession(sessionID string) bool {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	session, exists := sm.sessions[sessionID]
	if !exists || !session.IsRunning {
		return false // Session doesn't exist or is not running
	}

	// Close the stop channel to signal the download goroutine to stop
	close(session.StopChan)

	// Reset session state
	session.IsRunning = false
	atomic.StoreInt64(session.Speed, 0)

	// Create a new stop channel for the next start
	session.StopChan = make(chan struct{})

	log.Printf("Session %s download task stopped.", sessionID)
	return true
}

// --- Entry Management for Saved Data ---
// (EntryStore and SavedEntry structs remain unchanged)
type SavedEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PeerAddr string `json:"peerAddr"`
	InfoHash string `json:"infoHash"`
}
type EntryStore struct {
	mu      sync.RWMutex
	entries map[string]SavedEntry
	file    string
}

func NewEntryStore(file string) (*EntryStore, error) {
	store := &EntryStore{
		entries: make(map[string]SavedEntry),
		file:    file,
	}
	if err := store.load(); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if err := store.save(); err != nil {
			return nil, err
		}
	}
	return store, nil
}
func (s *EntryStore) load() error {
	data, err := os.ReadFile(s.file)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, &s.entries)
}
func (s *EntryStore) save() error {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.file, data, 0644)
}
func (s *EntryStore) GetAll() []SavedEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var entries []SavedEntry
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	return entries
}
func (s *EntryStore) Add(entry SavedEntry) (SavedEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return SavedEntry{}, err
	}
	entry.ID = hex.EncodeToString(b)
	s.entries[entry.ID] = entry
	if err := s.save(); err != nil {
		delete(s.entries, entry.ID)
		return SavedEntry{}, err
	}
	return entry, nil
}
func (s *EntryStore) Update(id string, updatedEntry SavedEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	originalEntry, exists := s.entries[id]
	if !exists {
		return fmt.Errorf("entry with ID %s not found", id)
	}
	updatedEntry.ID = id
	s.entries[id] = updatedEntry
	if err := s.save(); err != nil {
		s.entries[id] = originalEntry
		return err
	}
	return nil
}
func (s *EntryStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	originalEntry, exists := s.entries[id]
	if !exists {
		return fmt.Errorf("entry with ID %s not found", id)
	}
	delete(s.entries, id)
	if err := s.save(); err != nil {
		s.entries[id] = originalEntry
		return err
	}
	return nil
}

// --- Global Instances ---
var sessionManager = NewSessionManager()
var entryStore *EntryStore

// --- Utility Functions ---
func recoverPanic(w http.ResponseWriter) {
	if r := recover(); r != nil {
		log.Println("Recovered from panic:", r)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// --- HTTP Handlers ---
func entriesHandler(w http.ResponseWriter, r *http.Request) {
	defer recoverPanic(w)
	id := strings.TrimPrefix(r.URL.Path, "/api/entries/")
	switch r.Method {
	case http.MethodGet:
		entries := entryStore.GetAll()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	case http.MethodPost:
		var entry SavedEntry
		if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		newEntry, err := entryStore.Add(entry)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(newEntry)
	case http.MethodPut:
		if id == "" {
			http.Error(w, "Missing entry ID", http.StatusBadRequest)
			return
		}
		var entry SavedEntry
		if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := entryStore.Update(id, entry); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Entry updated")
	case http.MethodDelete:
		if id == "" {
			http.Error(w, "Missing entry ID", http.StatusBadRequest)
			return
		}
		if err := entryStore.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "Entry deleted")
	default:
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
	}
}

func handleReannounce(w http.ResponseWriter, r *http.Request) {
	defer recoverPanic(w)
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}
	var cmd *exec.Cmd
	if devMode {
		log.Println("Dev mode: running script from filesystem.")
		cwd, err := os.Getwd()
		if err != nil {
			http.Error(w, "Failed to get current working directory: "+err.Error(), http.StatusInternalServerError)
			return
		}
		scriptPath := filepath.Join(cwd, "py", "force_reannounce.py")
		cmd = exec.Command("python3", scriptPath)
	} else {
		log.Println("Production mode: running embedded script.")
		tmpfile, err := os.CreateTemp("", "force_reannounce_*.py")
		if err != nil {
			http.Error(w, "Failed to create temporary script file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer os.Remove(tmpfile.Name())
		if _, err := tmpfile.Write(pythonScript); err != nil {
			http.Error(w, "Failed to write script to temporary file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := tmpfile.Close(); err != nil {
			http.Error(w, "Failed to close temporary script file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		os.Chmod(tmpfile.Name(), 0700)
		cmd = exec.Command("python3", tmpfile.Name())
	}
	log.Println("Executing command:", cmd.String())
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Command failed with error: %v\nOutput:\n%s", err, string(output))
		http.Error(w, "Command failed: "+err.Error()+"\nOutput: "+string(output), http.StatusInternalServerError)
		return
	}
	log.Println("Script output:\n", string(output))
	w.Write([]byte("Re-announce completed with output:\n" + string(output)))
}

// --- Main Function ---
func main() {
	fmt.Println("fake_downloader version: 0.3.1 (Stateful Sessions)")
	var port string
	var defaultPeerAddr string
	var dbFile string

	flag.BoolVar(&devMode, "dev", false, "Enable development mode to load files from disk")
	flag.StringVar(&port, "port", "5000", "Port to listen on")
	flag.StringVar(&defaultPeerAddr, "addr", "127.0.0.1:63219", "Default peer address for the web UI input")
	flag.StringVar(&dbFile, "db", "entries.json", "Database file for saved entries")
	flag.Parse()

	var err error
	if devMode {
		log.Println("Running in DEVELOPMENT mode. Loading templates from filesystem.")
		templates, err = template.ParseFiles("templates/index.html")
	} else {
		log.Println("Running in PRODUCTION mode. Loading templates from embedded assets.")
		templates, err = template.ParseFS(templateFS, "templates/index.html")
	}
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	entryStore, err = NewEntryStore(dbFile)
	if err != nil {
		log.Fatalf("Failed to initialize entry store: %v", err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		data := struct {
			DefaultPeerAddr string
			Entries         []SavedEntry
		}{
			DefaultPeerAddr: defaultPeerAddr,
			Entries:         entryStore.GetAll(),
		}
		err := templates.Execute(w, data)
		if err != nil {
			http.Error(w, "Failed to render template", http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}

		sessionID := r.Header.Get("X-Session-ID")
		if sessionID == "" {
			http.Error(w, "Missing X-Session-ID header", http.StatusBadRequest)
			return
		}

		peerAddr := r.FormValue("peerAddr")
		infoHash := r.FormValue("infoHash")
		session := sessionManager.GetOrCreateSession(sessionID)

		if isRunning, _, _ := session.GetStatus(); isRunning {
			http.Error(w, "Session is already running", http.StatusConflict)
			return
		}

		session.SetRunning(true, peerAddr, infoHash)
		go func() {
			log.Printf("Starting download for session %s, peer %s", sessionID, peerAddr)
			err := util.ConnectPeerWithStop(peerAddr, infoHash, session.StopChan, session.Speed)
			if err != nil {
				log.Printf("Session %s connection ended: %v", sessionID, err)
			}
			// When the download ends (either by stop or error), update the state.
			session.SetRunning(false, "", "")
			atomic.StoreInt64(session.Speed, 0)
		}()
		log.Printf("Started session: %s", sessionID)
		w.Write([]byte("Started"))
	})

	http.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method != http.MethodPost {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
			return
		}

		sessionID := r.Header.Get("X-Session-ID")
		if sessionID == "" {
			http.Error(w, "Missing X-Session-ID header", http.StatusBadRequest)
			return
		}

		if sessionManager.StopSession(sessionID) {
			log.Printf("Stopped session: %s", sessionID)
			w.Write([]byte("Stopped"))
		} else {
			log.Printf("Attempted to stop a non-existent or already stopped session: %s", sessionID)
			w.Write([]byte("No active session found to stop"))
		}
	})

	http.HandleFunc("/speed", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		sessionID := r.Header.Get("X-Session-ID")
		if sessionID == "" {
			http.Error(w, "Missing X-Session-ID header", http.StatusBadRequest)
			return
		}

		session := sessionManager.GetOrCreateSession(sessionID)
		speed := atomic.LoadInt64(session.Speed)

		response := map[string]interface{}{"speed": speed}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})

	// NEW: /status endpoint
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		sessionID := r.Header.Get("X-Session-ID")
		if sessionID == "" {
			http.Error(w, "Missing X-Session-ID header", http.StatusBadRequest)
			return
		}

		session := sessionManager.GetOrCreateSession(sessionID)
		isRunning, peerAddr, infoHash := session.GetStatus()

		response := map[string]interface{}{
			"isRunning": isRunning,
			"peerAddr":  peerAddr,
			"infoHash":  infoHash,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})

	http.HandleFunc("/reannounce", handleReannounce)
	http.HandleFunc("/api/entries/", entriesHandler)

	log.Printf("Starting server on port: %s...", port)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}
