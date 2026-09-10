package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const AppIdentifier = "com.switchcodex.app"

func DataDir(development bool, root, home, override string) (string, error) {
	return dataDirForOS(runtime.GOOS, development, root, home, override, os.Getenv("LOCALAPPDATA"), os.Getenv("XDG_DATA_HOME"))
}

func dataDirForOS(goos string, development bool, root, home, override, localAppData, xdgDataHome string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	if development {
		return DevelopmentDataDir(root)
	}
	if goos == "windows" {
		if localAppData == "" {
			return "", errors.New("无法定位 LocalAppData")
		}
		return filepath.Join(localAppData, AppIdentifier, "data"), nil
	}
	if goos == "linux" {
		if xdgDataHome == "" {
			xdgDataHome = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(xdgDataHome, AppIdentifier, "data"), nil
	}
	return filepath.Join(home, "Library", "Application Support", AppIdentifier, "data"), nil
}

// A completed copy leaves a marker and the original directory intact. No
// directory merge is attempted when both independent indexes contain accounts.
func DevelopmentDataDir(root string) (string, error) {
	dst := filepath.Join(root, "data")
	src := filepath.Join(root, "src-tauri", "data")
	marker := filepath.Join(dst, ".migrated-from-tauri")
	if b, err := os.ReadFile(marker); err == nil && string(b) == src+"\n" {
		return dst, nil
	}
	oldExists := exists(filepath.Join(src, "accounts.json"))
	if !oldExists {
		return dst, nil
	}
	oldLock, err := Lock(filepath.Join(src, "scheduler.lock"))
	if err != nil {
		return "", err
	}
	defer oldLock.Close()
	newLock, err := Lock(filepath.Join(dst, "scheduler.lock"))
	if err != nil {
		return "", err
	}
	defer newLock.Close()
	oldHas, err := hasAccounts(src)
	if err != nil {
		return "", err
	}
	newHas, err := hasAccounts(dst)
	if err != nil {
		return "", err
	}
	if newHas && oldHas {
		return "", errors.New("data 与 src-tauri/data 均包含账号，请设置 CODEX_SWITCH_DATA_DIR 明确选择目录；未合并或覆盖任何账号")
	}
	if newHas {
		return dst, nil
	}
	// Stage and validate everything before modifying the destination. Only the
	// durable store is copied: no lock handles or scheduled-runtime credentials.
	staging, err := os.MkdirTemp(root, ".account-migration-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	for _, name := range []string{"accounts.json", "accounts", "settings.json", "openai-model-prices.json"} {
		from := filepath.Join(src, name)
		if !exists(from) {
			continue
		}
		if err = copyPrivate(from, filepath.Join(staging, name)); err != nil {
			return "", err
		}
	}
	for _, name := range []string{"accounts.json", "settings.json", "openai-model-prices.json"} {
		if b, e := os.ReadFile(filepath.Join(staging, name)); e == nil && !json.Valid(b) {
			return "", fmt.Errorf("旧 %s 不是有效 JSON，未迁移", name)
		}
	}
	if err = validateMigratedAccounts(staging); err != nil {
		return "", err
	}
	// Existing durable settings cannot be silently replaced by an older copy.
	for _, name := range []string{"settings.json", "openai-model-prices.json"} {
		a, e1 := os.ReadFile(filepath.Join(dst, name))
		b, e2 := os.ReadFile(filepath.Join(staging, name))
		if e1 == nil && e2 == nil && !bytes.Equal(a, b) {
			return "", fmt.Errorf("新旧 %s 不同，请设置 CODEX_SWITCH_DATA_DIR 选择目录", name)
		}
	}
	if exists(filepath.Join(dst, "accounts")) {
		entries, e := os.ReadDir(filepath.Join(dst, "accounts"))
		if e != nil {
			return "", e
		}
		for _, entry := range entries {
			if entry.Name() != ".gitkeep" && entry.Name() != ".DS_Store" {
				return "", errors.New("目标账号目录非空，未覆盖；请设置 CODEX_SWITCH_DATA_DIR")
			}
		}
	}
	// Publish only after the full source has been copied and validated. If any
	// destination write fails, restore every replaced file and remove files that
	// this attempt created so a retry starts from the same state.
	type previousFile struct {
		path string
		data []byte
		mode os.FileMode
	}
	var previous []previousFile
	var createdFiles, createdDirs []string
	committed := false
	defer func() {
		if committed {
			return
		}
		for i := len(createdFiles) - 1; i >= 0; i-- {
			_ = os.Remove(createdFiles[i])
		}
		for i := len(previous) - 1; i >= 0; i-- {
			_ = WriteAtomic(previous[i].path, previous[i].data, previous[i].mode)
		}
		for i := len(createdDirs) - 1; i >= 0; i-- {
			_ = os.Remove(createdDirs[i])
		}
	}()
	publish := func(from, to string) error {
		return filepath.WalkDir(from, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, relErr := filepath.Rel(from, path)
			if relErr != nil {
				return relErr
			}
			target := filepath.Join(to, rel)
			if entry.IsDir() {
				if !exists(target) {
					if mkdirErr := os.MkdirAll(target, 0700); mkdirErr != nil {
						return mkdirErr
					}
					createdDirs = append(createdDirs, target)
				}
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if old, readOldErr := os.ReadFile(target); readOldErr == nil {
				if bytes.Equal(old, data) {
					return nil
				}
				info, statErr := os.Stat(target)
				if statErr != nil {
					return statErr
				}
				previous = append(previous, previousFile{target, old, info.Mode().Perm()})
			} else if errors.Is(readOldErr, os.ErrNotExist) {
				createdFiles = append(createdFiles, target)
			} else {
				return readOldErr
			}
			return WriteAtomic(target, data, 0600)
		})
	}
	for _, name := range []string{"accounts", "settings.json", "openai-model-prices.json", "accounts.json"} {
		from := filepath.Join(staging, name)
		if exists(from) {
			if err = publish(from, filepath.Join(dst, name)); err != nil {
				return "", err
			}
		}
	}
	if err = WriteAtomic(marker, []byte(src+"\n"), 0600); err != nil {
		return "", err
	}
	committed = true
	return dst, nil
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func hasAccounts(dir string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, "accounts.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var v struct {
		Accounts []json.RawMessage `json:"accounts"`
	}
	if err = json.Unmarshal(b, &v); err != nil {
		return false, fmt.Errorf("无法读取 %s 的账号索引: %w", dir, err)
	}
	return len(v.Accounts) > 0, nil
}

func validateMigratedAccounts(staging string) error {
	b, err := os.ReadFile(filepath.Join(staging, "accounts.json"))
	if err != nil {
		return fmt.Errorf("无法读取旧账号索引: %w", err)
	}
	var index struct {
		ActiveAccountID *string `json:"activeAccountId"`
		Accounts        []struct {
			ID string `json:"id"`
		} `json:"accounts"`
	}
	if err = json.Unmarshal(b, &index); err != nil {
		return fmt.Errorf("旧账号索引无效: %w", err)
	}
	seen := make(map[string]bool, len(index.Accounts))
	for _, account := range index.Accounts {
		if account.ID == "" || account.ID == "." || account.ID == ".." || strings.ContainsAny(account.ID, "/\\") || seen[account.ID] {
			return errors.New("旧账号索引包含非法或重复 ID，未迁移")
		}
		seen[account.ID] = true
		auth, readErr := os.ReadFile(filepath.Join(staging, "accounts", account.ID, "auth.json"))
		if readErr != nil {
			return fmt.Errorf("旧账号 %s 缺少 auth.json，未迁移", account.ID)
		}
		var doc map[string]json.RawMessage
		if json.Unmarshal(auth, &doc) != nil || doc == nil || !hasMigratableCredential(doc) {
			return fmt.Errorf("旧账号 %s 的 auth.json 无效，未迁移", account.ID)
		}
	}
	if index.ActiveAccountID != nil && !seen[*index.ActiveAccountID] {
		return errors.New("旧账号索引的当前账号不存在，未迁移")
	}
	return nil
}

func hasMigratableCredential(doc map[string]json.RawMessage) bool {
	var key string
	if json.Unmarshal(doc["OPENAI_API_KEY"], &key) == nil && strings.TrimSpace(key) != "" {
		return true
	}
	var tokens map[string]json.RawMessage
	if json.Unmarshal(doc["tokens"], &tokens) != nil {
		return false
	}
	var refresh string
	return json.Unmarshal(tokens["refresh_token"], &refresh) == nil && strings.TrimSpace(refresh) != ""
}
func copyPrivate(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("旧账号目录包含符号链接，未自动迁移")
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = WriteAtomic(target, data, 0600); err != nil {
			return err
		}
		copy, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, copy) {
			return errors.New("账号数据复制校验失败")
		}
		return nil
	})
}
