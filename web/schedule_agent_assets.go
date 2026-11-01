package web

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/inputref"
)

// freezeScheduleParts copies file bytes to the authenticated account's immutable
// Information Store. Jobs never depend on the lifetime of a source conversation.
func (m *Manager) freezeScheduleParts(owner *identity.Scope, cwd string, parts []inputref.Part) ([]inputref.Part, error) {
	out := inputref.Clone(parts)
	var store *information.Store
	var root *os.Root
	defer func() {
		if root != nil {
			root.Close()
		}
	}()
	mapped := map[string]string{}
	var walk func([]inputref.Part) error
	walk = func(ps []inputref.Part) error {
		for i := range ps {
			p := &ps[i]
			if p.Type == "file" {
				if cached, ok := mapped[p.Path]; ok {
					p.Path = cached
					continue
				}
				if store == nil {
					var err error
					store, err = m.InformationStore(owner)
					if err != nil {
						return err
					}
				}
				old := p.Path
				if strings.HasPrefix(old, "information-assets/") {
					f, _, err := store.AssetRead(old)
					if err != nil {
						return fmt.Errorf("资料附件不可访问: %w", err)
					}
					f.Close()
				} else {
					if root == nil {
						var err error
						root, err = os.OpenRoot(cwd)
						if err != nil {
							return err
						}
					}
					if err := scheduleRegularSource(root, old); err != nil {
						return err
					}
					f, err := root.Open(old)
					if err != nil {
						return fmt.Errorf("附件 %s 不存在或无法读取", old)
					}
					before, err := f.Stat()
					if err != nil || !before.Mode().IsRegular() {
						f.Close()
						return errors.New("附件必须为工作区内的普通文件")
					}
					a, err := store.PutAsset(filepath.Base(old), "", f, information.MaxAssetBytes)
					after, statErr := f.Stat()
					f.Close()
					latest, pathErr := root.Lstat(old)
					if e := scheduleRegularSource(root, old); e != nil {
						return e
					}
					if err != nil {
						return err
					}
					if statErr != nil || pathErr != nil || !os.SameFile(before, latest) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
						return errors.New("附件在保存计划时被修改，请重试")
					}
					p.Path = a.Asset
				}
				mapped[old] = p.Path
			}
			if err := walk(p.Parts); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Manager) materializeScheduleParts(owner *identity.Scope, cwd string, parts []inputref.Part) ([]inputref.Part, error) {
	out := inputref.Clone(parts)
	var handles []string
	for _, p := range inputref.Flatten(out) {
		if p.Type == "file" {
			if !strings.HasPrefix(p.Path, "information-assets/") {
				return nil, errors.New("任务附件未持久保存，请重新编辑任务")
			}
			handles = append(handles, p.Path)
		}
	}
	if len(handles) == 0 {
		return out, nil
	}
	store, err := m.InformationStore(owner)
	if err != nil {
		return nil, err
	}
	batch, err := store.BeginMaterialization(cwd)
	if err != nil {
		return nil, err
	}
	defer batch.Rollback()
	mapped := map[string]string{}
	for _, handle := range handles {
		if _, ok := mapped[handle]; ok {
			continue
		}
		a, err := batch.MaterializeAsset(handle)
		if err != nil {
			return nil, err
		}
		mapped[handle] = a.Path
	}
	if err = batch.Commit(); err != nil {
		return nil, err
	}
	var walk func([]inputref.Part)
	walk = func(ps []inputref.Part) {
		for i := range ps {
			if ps[i].Type == "file" {
				ps[i].Path = mapped[ps[i].Path]
			}
			walk(ps[i].Parts)
		}
	}
	walk(out)
	return out, nil
}

func scheduleRegularSource(root *os.Root, relative string) error {
	current := ""
	for i, part := range strings.Split(relative, "/") {
		current = path.Join(current, part)
		st, err := root.Lstat(current)
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("附件 %s 含符号链接或不可访问", relative)
		}
		if i == len(strings.Split(relative, "/"))-1 {
			if !st.Mode().IsRegular() {
				return errors.New("附件不是普通文件")
			}
		} else if !st.IsDir() {
			return errors.New("附件目录无效")
		}
	}
	return nil
}
