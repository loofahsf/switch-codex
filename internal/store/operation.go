package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type operationMarker struct {
	Kind            string `json:"kind"`
	BeforeSHA256    string `json:"beforeSha256"`
	AfterSHA256     string `json:"afterSha256"`
	ID              string `json:"id,omitempty"`
	Tomb            string `json:"tomb,omitempty"`
	OldTarget       []byte `json:"oldTarget,omitempty"`
	OldTargetExists bool   `json:"oldTargetExists,omitempty"`
	NewTargetSHA256 string `json:"newTargetSha256,omitempty"`
	CreatedID       string `json:"createdId,omitempty"`
}

func digestHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Store) operationPath() string {
	return filepath.Join(s.DataDir, "accounts-operation.pending.json")
}

func (s *Store) beginOperation(before, after index, marker operationMarker) error {
	if err := s.recoverOperation(); err != nil {
		return err
	}
	current, err := os.ReadFile(s.indexPath())
	if err != nil {
		return err
	}
	marker.BeforeSHA256 = digestHex(current)
	next, err := marshalIndex(after)
	if err != nil {
		return err
	}
	marker.AfterSHA256 = digestHex(next)
	if after.Revision != before.Revision+1 {
		return errors.New("账号事务版本不连续")
	}
	raw, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	return s.write(s.operationPath(), append(raw, '\n'), 0600)
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func validMarker(m operationMarker) bool {
	if !validDigest(m.BeforeSHA256) || !validDigest(m.AfterSHA256) || m.BeforeSHA256 == m.AfterSHA256 {
		return false
	}
	switch m.Kind {
	case "delete":
		if !validAccountID(m.ID) {
			return false
		}
		prefix := m.ID + ".deleted-"
		if !strings.HasPrefix(m.Tomb, prefix) || filepath.Base(m.Tomb) != m.Tomb {
			return false
		}
		_, err := uuid.Parse(strings.TrimPrefix(m.Tomb, prefix))
		return err == nil && m.CreatedID == "" && m.NewTargetSHA256 == ""
	case "switch":
		if !validDigest(m.NewTargetSHA256) || m.ID != "" || m.Tomb != "" {
			return false
		}
		if m.CreatedID != "" {
			if _, err := uuid.Parse(m.CreatedID); err != nil {
				return false
			}
		}
		return m.OldTargetExists || len(m.OldTarget) == 0
	default:
		return false
	}
}

func (s *Store) restoreTargetIfOwned(m operationMarker) error {
	current, err := os.ReadFile(s.TargetAuthPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if digestHex(current) != m.NewTargetSHA256 {
		return nil
	}
	if m.OldTargetExists {
		return s.write(s.TargetAuthPath, m.OldTarget, 0600)
	}
	if err = os.Remove(s.TargetAuthPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) recoverOperation() error {
	raw, err := os.ReadFile(s.operationPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取账号事务失败: %w", err)
	}
	var m operationMarker
	if json.Unmarshal(raw, &m) != nil || !validMarker(m) {
		return errors.New("账号事务标记损坏，已保留凭据")
	}
	indexRaw, err := os.ReadFile(s.indexPath())
	if err != nil {
		return fmt.Errorf("读取账号事务索引失败: %w", err)
	}
	digest := digestHex(indexRaw)
	if digest != m.BeforeSHA256 && digest != m.AfterSHA256 {
		return errors.New("账号事务索引与标记不一致，已保留凭据")
	}
	var current index
	if err = json.Unmarshal(indexRaw, &current); err != nil {
		return fmt.Errorf("账号事务索引损坏: %w", err)
	}
	committed := digest == m.AfterSHA256
	if m.Kind == "delete" {
		dir := filepath.Dir(s.authPath(m.ID))
		tomb := filepath.Join(filepath.Dir(dir), m.Tomb)
		if committed {
			if find(current, m.ID) >= 0 {
				return errors.New("已提交删除事务仍含账号，已保留凭据")
			}
			if err = os.RemoveAll(tomb); err != nil {
				return err
			}
		} else {
			if find(current, m.ID) < 0 {
				return errors.New("未提交删除事务缺少账号，已保留凭据")
			}
			if _, err = os.Stat(tomb); err == nil {
				if _, destErr := os.Stat(dir); destErr == nil {
					return errors.New("账号目录与墓碑同时存在，已保留凭据")
				} else if !errors.Is(destErr, os.ErrNotExist) {
					return destErr
				}
				if err = s.rename(tomb, dir); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	} else if !committed {
		if err = s.restoreTargetIfOwned(m); err != nil {
			return err
		}
		if m.CreatedID != "" {
			if find(current, m.CreatedID) >= 0 {
				return errors.New("未提交新增事务账号已在索引中，已保留凭据")
			}
			if err = os.RemoveAll(filepath.Dir(s.authPath(m.CreatedID))); err != nil {
				return err
			}
		}
	}
	return s.removeMarker(s.operationPath())
}

func (s *Store) checkTarget(expected []byte) error {
	current, err := os.ReadFile(s.TargetAuthPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrTargetAuthChanged
		}
		return err
	}
	normalized, err := NormalizeAuth(current)
	if err != nil || !bytes.Equal(normalized, expected) {
		return ErrTargetAuthChanged
	}
	return nil
}
