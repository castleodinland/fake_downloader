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

// --- 嵌入资源 ---
//go:embed templates/*
var templateFS embed.FS

//go:embed py/force_reannounce.py
var pythonScript []byte

// --- 全局变量 ---
var (
	templates *template.Template
	devMode   bool
)

// --- Session Management ---
// Session represents a user's connection session.
type Session struct {
	ID       string
	StopChan chan struct{}
	Speed    *int64
	// BUGFIX: Removed CreatedAt field as sessions are now permanent until explicitly stopped.
}

// SessionManager manages active sessions.
type SessionManager struct {
	sessions map[string]*Session
	mutex    sync.RWMutex
}

// NewSessionManager creates a new session manager.
func NewSessionManager() *SessionManager {
	sm := &SessionManager{
		sessions: make(map[string]*Session),
	}
	// BUGFIX: Removed the call to the session cleanup goroutine.
	// Sessions will no longer expire automatically.
	return sm
}

// CreateSession creates and stores a new session.
func (sm *SessionManager) CreateSession(sessionID string) *Session {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	// If a session for this ID already exists, stop the old one before creating a new one.
	if existingSession, exists := sm.sessions[sessionID]; exists {
		if existingSession.StopChan != nil {
			// Prevent closing a channel that might already be closed.
			select {
			case <-existingSession.StopChan:
				// Already closed
			default:
				close(existingSession.StopChan)
			}
		}
	}
	session := &Session{
		ID:       sessionID,
		StopChan: make(chan struct{}),
		Speed:    new(int64),
	}
	sm.sessions[sessionID] = session
	return session
}

// GetSession retrieves a session by ID.
func (sm *SessionManager) GetSession(sessionID string) (*Session, bool) {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()
	session, exists := sm.sessions[sessionID]
	return session, exists
}

// StopSession stops a running session. This is now the ONLY way a session is removed.
func (sm *SessionManager) StopSession(sessionID string) bool {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	session, exists := sm.sessions[sessionID]
	if !exists {
		return false
	}
	if session.StopChan != nil {
		select {
		case <-session.StopChan:
			// Already closed
		default:
			close(session.StopChan)
		}
	}
	delete(sm.sessions, sessionID)
	return true
}

// BUGFIX: The entire cleanupExpiredSessions function has been removed to ensure sessions are permanent.

// --- Entry Management for Saved Data ---
// SavedEntry represents a saved entry in our JSON database.
type SavedEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PeerAddr string `json:"peerAddr"`
	InfoHash string `json:"infoHash"`
}

// EntryStore manages the collection of saved entries.
type EntryStore struct {
	mu      sync.RWMutex
	entries map[string]SavedEntry
	file    string
}

// NewEntryStore creates and loads an entry store from a file.
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

// load reads the database file into memory.
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

// save writes the current entries from memory to the database file.
func (s *EntryStore) save() error {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.file, data, 0644)
}

// GetAll returns a slice of all saved entries.
func (s *EntryStore) GetAll() []SavedEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var entries []SavedEntry
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	return entries
}

// Add creates a new entry and saves it.
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

// Update modifies an existing entry.
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

// Delete removes an entry.
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
func getSessionID(r *http.Request) string {
	return r.RemoteAddr + "_" + r.UserAgent()
}

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
		if r.Method == http.MethodGet {
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
		}
	})

	http.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method == http.MethodPost {
			sessionID := getSessionID(r)
			peerAddr := r.FormValue("peerAddr")
			infoHash := r.FormValue("infoHash")
			session := sessionManager.CreateSession(sessionID)
			go func() {
				err := util.ConnectPeerWithStop(peerAddr, infoHash, session.StopChan, session.Speed)
				if err != nil {
					// This error occurs when the connection ends, e.g., after being stopped.
					// It's not necessarily a critical failure.
					log.Printf("Session %s connection ended: %v", sessionID, err)
				}
			}()
			log.Printf("Started session: %s", sessionID)
			w.Write([]byte("Started"))
		} else {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method == http.MethodPost {
			sessionID := getSessionID(r)
			if sessionManager.StopSession(sessionID) {
				log.Printf("Stopped session: %s", sessionID)
				w.Write([]byte("Stopped"))
			} else {
				log.Printf("Attempted to stop a non-existent session: %s", sessionID)
				w.Write([]byte("No active session found"))
			}
		} else {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/speed", func(w http.ResponseWriter, r *http.Request) {
		defer recoverPanic(w)
		if r.Method == http.MethodGet {
			sessionID := getSessionID(r)
			session, exists := sessionManager.GetSession(sessionID)
			var speed int64 = 0
			if exists {
				speed = atomic.LoadInt64(session.Speed)
			}
			response := map[string]int64{"speed": speed}
			jsonResponse, err := json.Marshal(response)
			if err != nil {
				http.Error(w, "Failed to create JSON response", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(jsonResponse)
		} else {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/reannounce", handleReannounce)

	http.HandleFunc("/api/entries/", entriesHandler)

	log.Printf("Starting server on port: %s...", port)
	log.Fatal(http.ListenAndServe("0.0.0.0:"+port, nil))
}

