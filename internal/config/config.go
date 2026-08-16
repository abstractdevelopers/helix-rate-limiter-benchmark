package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Limit defines a rate limit for a client tier.
type Limit struct {
	Requests     int `json:"requests"`
	WindowSeconds int `json:"window_seconds"`
}

// Config holds all rate limit configurations.
type Config struct {
	DefaultLimit Limit                   `json:"default_limit"`
	Clients      map[string]*Limit       `json:"clients"`
}

// Manager loads and manages rate limit configuration with hot-reload support.
type Manager struct {
	mu      sync.RWMutex
	config  *Config
	filePath string
}

// NewManager creates a new config manager and loads from the given file.
func NewManager(filePath string) (*Manager, error) {
	m := &Manager{
		filePath: filePath,
	}
	if err := m.Load(); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return m, nil
}

// Load reads the configuration file and validates it.
func (m *Manager) Load() error {
	data, err := os.ReadFile(m.filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	m.mu.Lock()
	m.config = &cfg
	m.mu.Unlock()
	return nil
}

// Reload re-reads the configuration file. Returns error if the new config is invalid.
func (m *Manager) Reload() error {
	return m.Load()
}

// Get returns a snapshot of the current configuration.
func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Return a deep copy to avoid race conditions.
	return m.config.Clone()
}

// GetLimit returns the rate limit for a given client tier.
func (m *Manager) GetLimit(tier string) *Limit {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if tier == "" {
		return m.config.DefaultLimit.Clone()
	}
	if l, ok := m.config.Clients[tier]; ok {
		return l.Clone()
	}
	return m.config.DefaultLimit.Clone()
}

// SetLimit adds or updates a client tier limit.
func (m *Manager) SetLimit(tier string, limit *Limit) error {
	if tier == "" {
		return fmt.Errorf("tier name cannot be empty")
	}
	if err := limit.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.config.Clients[tier] = limit.Clone()
	return m.SaveLocked()
}

// RemoveLimit removes a client tier limit.
func (m *Manager) RemoveLimit(tier string) error {
	if tier == "" {
		return fmt.Errorf("tier name cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.config.Clients, tier)
	return m.SaveLocked()
}

// SaveLocked writes the current config to disk. Caller must hold m.mu (write lock).
func (m *Manager) SaveLocked() error {
	data, err := json.MarshalIndent(m.config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(m.filePath, data, 0644)
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	if err := c.DefaultLimit.Validate(); err != nil {
		return fmt.Errorf("default_limit: %w", err)
	}
	for name, l := range c.Clients {
		if err := l.Validate(); err != nil {
			return fmt.Errorf("clients.%s: %w", name, err)
		}
	}
	return nil
}

// Validate checks if a limit is valid.
func (l *Limit) Validate() error {
	if l.Requests <= 0 {
		return fmt.Errorf("requests must be positive, got %d", l.Requests)
	}
	if l.WindowSeconds <= 0 {
		return fmt.Errorf("window_seconds must be positive, got %d", l.WindowSeconds)
	}
	return nil
}

// Clone returns a deep copy of the Limit.
func (l *Limit) Clone() *Limit {
	return &Limit{
		Requests:      l.Requests,
		WindowSeconds: l.WindowSeconds,
	}
}

// Clone returns a deep copy of the Config.
func (c *Config) Clone() *Config {
	clients := make(map[string]*Limit, len(c.Clients))
	for k, v := range c.Clients {
		clients[k] = v.Clone()
	}
	return &Config{
		DefaultLimit: *c.DefaultLimit.Clone(),
		Clients:      clients,
	}
}