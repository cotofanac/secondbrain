package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Daily snapshots of the database, written next to it under backups/. They
// guard against mistakes and bad upgrades, not against losing the disk: copy
// the backups directory somewhere else as well.
var (
	backupDir  string
	backupKeep = 7
)

const backupPrefix, backupSuffix = "secondbrain-", ".db"

// configureBackups reads BACKUP_KEEP (snapshots to retain; 0 disables).
func configureBackups(dataDir string) {
	if raw := strings.TrimSpace(os.Getenv("BACKUP_KEEP")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			log.Fatal("BACKUP_KEEP must be a non-negative integer")
		}
		backupKeep = n
	}
	backupDir = filepath.Join(dataDir, "backups")
	if backupKeep == 0 {
		log.Printf("Daily backups disabled")
		return
	}
	log.Printf("Daily backups: keeping %d in %s", backupKeep, backupDir)
}

// runDailyBackup writes today's snapshot if it does not exist yet and prunes
// the oldest beyond backupKeep. It reports the path written, if any.
func runDailyBackup(now time.Time) (string, error) {
	if backupKeep == 0 || backupDir == "" {
		return "", nil
	}
	if err := os.MkdirAll(backupDir, 0750); err != nil {
		return "", err
	}
	path := filepath.Join(backupDir, backupPrefix+now.Format("2006-01-02")+backupSuffix)
	written := ""
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// VACUUM INTO produces a consistent, compacted copy while the app keeps
		// running. Write to a temporary name first so a crash never leaves a
		// truncated file that looks like a finished backup.
		tmp := path + ".tmp"
		os.Remove(tmp)
		if _, err := db.Exec(`VACUUM INTO ?`, tmp); err != nil {
			os.Remove(tmp)
			return "", fmt.Errorf("snapshot: %w", err)
		}
		if err := os.Chmod(tmp, 0600); err != nil {
			os.Remove(tmp)
			return "", err
		}
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			return "", err
		}
		written = path
	} else if err != nil {
		return "", err
	}
	return written, pruneBackups()
}

func pruneBackups() error {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(name, backupPrefix) && strings.HasSuffix(name, backupSuffix) {
			if _, err := time.Parse("2006-01-02", strings.TrimSuffix(strings.TrimPrefix(name, backupPrefix), backupSuffix)); err == nil {
				names = append(names, name)
			}
		}
	}
	// Date-stamped names sort chronologically.
	sort.Strings(names)
	for len(names) > backupKeep {
		if err := os.Remove(filepath.Join(backupDir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}
