package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Profile errors.
var (
	ErrProfileNotFound    = errors.New("profile not found")
	ErrProfileExists      = errors.New("profile already exists")
	ErrNoAuthFiles        = errors.New("no auth files found to backup")
	ErrInvalidProfileName = errors.New("invalid profile name")
)

// Profile represents a saved credential profile.
type Profile struct {
	// Adapter is the AI coding agent this profile is for.
	Adapter Adapter `json:"adapter"`

	// Name is the profile name (e.g., "work", "personal", "user@email.com").
	Name string `json:"name"`

	// Path is the full path to the profile directory.
	Path string `json:"path"`

	// CreatedAt is when the profile was first created.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the profile was last updated.
	UpdatedAt time.Time `json:"updated_at"`

	// AuthFiles lists the auth file names stored in this profile.
	AuthFiles []string `json:"auth_files"`

	// ContentHash is the SHA-256 hash of all auth file contents.
	// Used for detecting which profile is currently active.
	ContentHash string `json:"content_hash"`
}

// ProfileMeta is stored as meta.json in each profile directory.
type ProfileMeta struct {
	Adapter     string    `json:"adapter"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	AuthFiles   []string  `json:"auth_files"`
	ContentHash string    `json:"content_hash"`
}

// Backup copies current auth files to a profile in the vault.
func Backup(vaultPath string, adapter Adapter, profileName string) (*Profile, error) {
	if profileName == "" {
		return nil, ErrInvalidProfileName
	}

	authPaths := GetAuthPaths(adapter)
	existingPaths := authPaths.ExistingPaths()
	if len(existingPaths) == 0 {
		return nil, ErrNoAuthFiles
	}

	profilePath := ProfilePath(vaultPath, adapter, profileName)

	// Create profile directory
	if err := os.MkdirAll(profilePath, 0700); err != nil {
		return nil, fmt.Errorf("failed to create profile directory: %w", err)
	}

	now := time.Now().UTC()
	profile := &Profile{
		Adapter:   adapter,
		Name:      profileName,
		Path:      profilePath,
		CreatedAt: now,
		UpdatedAt: now,
		AuthFiles: make([]string, 0, len(existingPaths)),
	}

	// Check if profile already exists (for CreatedAt preservation)
	metaPath := filepath.Join(profilePath, "meta.json")
	if metaData, err := os.ReadFile(metaPath); err == nil {
		var existingMeta ProfileMeta
		if err := json.Unmarshal(metaData, &existingMeta); err == nil {
			profile.CreatedAt = existingMeta.CreatedAt
		}
	}

	// Copy each auth file
	var hashInputs []byte
	for _, srcPath := range existingPaths {
		fileName := filepath.Base(srcPath)
		dstPath := filepath.Join(profilePath, fileName)

		data, err := os.ReadFile(srcPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read auth file %s: %w", srcPath, err)
		}

		if err := os.WriteFile(dstPath, data, 0600); err != nil {
			return nil, fmt.Errorf("failed to write auth file %s: %w", dstPath, err)
		}

		profile.AuthFiles = append(profile.AuthFiles, fileName)
		hashInputs = append(hashInputs, data...)
	}

	// Calculate content hash
	hash := sha256.Sum256(hashInputs)
	profile.ContentHash = hex.EncodeToString(hash[:])

	// Write metadata
	meta := ProfileMeta{
		Adapter:     string(adapter),
		Name:        profileName,
		CreatedAt:   profile.CreatedAt,
		UpdatedAt:   profile.UpdatedAt,
		AuthFiles:   profile.AuthFiles,
		ContentHash: profile.ContentHash,
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}
	if err := os.WriteFile(metaPath, metaJSON, 0600); err != nil {
		return nil, fmt.Errorf("failed to write metadata: %w", err)
	}

	return profile, nil
}

// Activate restores auth files from a profile to their system locations.
func Activate(vaultPath string, adapter Adapter, profileName string) error {
	if profileName == "" {
		return ErrInvalidProfileName
	}

	profilePath := ProfilePath(vaultPath, adapter, profileName)

	// Check profile exists
	if _, err := os.Stat(profilePath); os.IsNotExist(err) {
		return ErrProfileNotFound
	}

	// Load metadata to get auth files list
	metaPath := filepath.Join(profilePath, "meta.json")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("failed to read profile metadata: %w", err)
	}

	var meta ProfileMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return fmt.Errorf("failed to parse profile metadata: %w", err)
	}

	authPaths := GetAuthPaths(adapter)

	// Map auth file names to their destination paths
	destMap := make(map[string]string)
	destMap[filepath.Base(authPaths.Primary)] = authPaths.Primary
	for _, secondary := range authPaths.Secondary {
		destMap[filepath.Base(secondary)] = secondary
	}

	// Restore each auth file
	for _, fileName := range meta.AuthFiles {
		srcPath := filepath.Join(profilePath, fileName)
		dstPath, ok := destMap[fileName]
		if !ok {
			// Unknown file, try to find a matching destination
			if fileName == ".claude.json" || fileName == "auth.json" || fileName == "settings.json" {
				// Use primary path for main auth files
				dstPath = authPaths.Primary
				if fileName == "auth.json" && len(authPaths.Secondary) > 0 {
					// For Claude, auth.json goes to secondary location
					for _, sec := range authPaths.Secondary {
						if filepath.Base(sec) == fileName {
							dstPath = sec
							break
						}
					}
				}
			} else {
				continue // Skip unknown files
			}
		}

		// Ensure destination directory exists
		if err := os.MkdirAll(filepath.Dir(dstPath), 0700); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", dstPath, err)
		}

		// Copy file
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", srcPath, err)
		}

		if err := os.WriteFile(dstPath, data, 0600); err != nil {
			return fmt.Errorf("failed to write %s: %w", dstPath, err)
		}
	}

	return nil
}

// Delete removes a profile from the vault.
func Delete(vaultPath string, adapter Adapter, profileName string) error {
	if profileName == "" {
		return ErrInvalidProfileName
	}

	profilePath := ProfilePath(vaultPath, adapter, profileName)

	if _, err := os.Stat(profilePath); os.IsNotExist(err) {
		return ErrProfileNotFound
	}

	return os.RemoveAll(profilePath)
}

// Get retrieves a profile by adapter and name.
func Get(vaultPath string, adapter Adapter, profileName string) (*Profile, error) {
	if profileName == "" {
		return nil, ErrInvalidProfileName
	}

	profilePath := ProfilePath(vaultPath, adapter, profileName)

	metaPath := filepath.Join(profilePath, "meta.json")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrProfileNotFound
		}
		return nil, fmt.Errorf("failed to read profile metadata: %w", err)
	}

	var meta ProfileMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse profile metadata: %w", err)
	}

	return &Profile{
		Adapter:     adapter,
		Name:        profileName,
		Path:        profilePath,
		CreatedAt:   meta.CreatedAt,
		UpdatedAt:   meta.UpdatedAt,
		AuthFiles:   meta.AuthFiles,
		ContentHash: meta.ContentHash,
	}, nil
}

// List returns all profiles for an adapter (or all adapters if adapter is empty).
func List(vaultPath string, adapter Adapter) ([]*Profile, error) {
	var profiles []*Profile

	adapters := []Adapter{adapter}
	if adapter == "" {
		adapters = AllAdapters()
	}

	for _, a := range adapters {
		providerPath := filepath.Join(ProfilesPath(vaultPath), a.Provider())

		entries, err := os.ReadDir(providerPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("failed to read profiles for %s: %w", a, err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			profile, err := Get(vaultPath, a, entry.Name())
			if err != nil {
				continue // Skip invalid profiles
			}
			profiles = append(profiles, profile)
		}
	}

	return profiles, nil
}

// GetActive returns the currently active profile for an adapter by comparing
// content hashes of current auth files against stored profiles.
func GetActive(vaultPath string, adapter Adapter) (*Profile, error) {
	// Get current auth file hash
	currentHash, err := getCurrentAuthHash(adapter)
	if err != nil || currentHash == "" {
		return nil, nil // No auth files or error, no active profile
	}

	// List all profiles and find matching hash
	profiles, err := List(vaultPath, adapter)
	if err != nil {
		return nil, err
	}

	for _, profile := range profiles {
		if profile.ContentHash == currentHash {
			return profile, nil
		}
	}

	return nil, nil // No matching profile
}

// Clear removes the current auth files for an adapter.
func Clear(adapter Adapter) error {
	authPaths := GetAuthPaths(adapter)

	for _, path := range authPaths.AllPaths() {
		if _, err := os.Stat(path); err == nil {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("failed to remove %s: %w", path, err)
			}
		}
	}

	return nil
}

// getCurrentAuthHash calculates the content hash of current auth files.
func getCurrentAuthHash(adapter Adapter) (string, error) {
	authPaths := GetAuthPaths(adapter)
	existingPaths := authPaths.ExistingPaths()

	if len(existingPaths) == 0 {
		return "", nil
	}

	var hashInputs []byte
	for _, path := range existingPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		hashInputs = append(hashInputs, data...)
	}

	hash := sha256.Sum256(hashInputs)
	return hex.EncodeToString(hash[:]), nil
}

// CopyFile copies a file from src to dst, preserving permissions.
func CopyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	// Create destination directory if needed
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
