package main

import (
	"errors"
	"os"
	"path/filepath"
)

func pinnedInput(path, expected string) ([]byte, error) {
	raw, err := readInput(path)
	if err != nil {
		return nil, err
	}
	if len(expected) != 64 || hash(raw) != expected {
		return nil, errors.New("固定した研究入力のhashが一致しません")
	}
	return raw, nil
}

// Content-addressed artifacts keep references stable across offline reruns.
// Only a freshly generated duplicate is removed; existing data is hash checked.
func saveContentAddressed(prefix string, value any) (string, []byte, error) {
	path, raw, err := save(prefix, value)
	if err != nil {
		return "", nil, err
	}
	stable := filepath.Join(filepath.Dir(path), prefix+"-"+hash(raw)+".json")
	if _, err := os.Lstat(stable); err == nil {
		existing, err := readInput(stable)
		if err != nil || hash(existing) != hash(raw) {
			return "", nil, errors.New("保存済み研究データのhashを確認してください")
		}
		if os.Remove(path) != nil {
			return "", nil, errors.New("重複した研究出力を整理できません")
		}
	} else if !os.IsNotExist(err) {
		return "", nil, errors.New("研究出力先を確認できません")
	} else if os.Rename(path, stable) != nil {
		return "", nil, errors.New("研究データを確定できません")
	}
	return stable, raw, nil
}
