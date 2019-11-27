// Copyright 2019 Keybase Inc. All rights reserved.
// Use of this source code is governed by a BSD
// license that can be found in the LICENSE file.

package search

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/blevesearch/bleve"
	"github.com/keybase/client/go/kbfs/data"
	"github.com/keybase/client/go/kbfs/libfs"
	"github.com/keybase/client/go/kbfs/libkbfs"
	"github.com/keybase/client/go/kbfs/tlf"
	"github.com/pkg/errors"
)

type Indexer struct {
	config libkbfs.Config
	index  bleve.Index
}

func indexPath(root string) string {
	return filepath.Join(root, "kbfs_index")
}

func NewIndexer(ctx context.Context, config libkbfs.Config) (*Indexer, error) {
	params, err := Params(config.KbContext(), config.StorageRoot())
	if err != nil {
		return nil, err
	}
	ctx, indexConfig, err := Init(
		ctx, config.KbContext(), params, nil, "", config.VLogLevel())
	if err != nil {
		return nil, err
	}

	session, err := config.KBPKI().GetCurrentSession(ctx)
	if err != nil {
		return nil, err
	}
	privateHandle, err := libkbfs.GetHandleFromFolderNameAndType(
		ctx, config.KBPKI(), config.MDOps(), config, string(session.Name),
		tlf.Private)
	if err != nil {
		return nil, err
	}

	fs, err := libfs.NewFS(
		ctx, indexConfig, privateHandle, data.MasterBranch, "", "", 0)
	if err != nil {
		return nil, err
	}
	err = fs.MkdirAll("kbfs_index", 0400)
	if err != nil {
		return nil, err
	}
	fs, err = fs.ChrootAsLibFS("kbfs_index")
	if err != nil {
		return nil, err
	}

	var index bleve.Index
	bleveConfig := map[string]interface{}{
		"openFile": func(p string, f int, m os.FileMode) (io.ReadWriteCloser, error) { return fs.OpenFile(p, f, m) },
		"mkdir":    fs.MkdirAll,
	}
	p := "kbindex"
	_, err = fs.Stat(p)
	switch {
	case os.IsNotExist(errors.Cause(err)):
		mapping := bleve.NewIndexMapping()
		index, err = bleve.NewUsing(
			p, mapping, "scorch", "kbindex", bleveConfig)
		if err != nil {
			return nil, err
		}
	case err == nil:
		index, err = bleve.OpenUsing(p, bleveConfig)
		if err != nil {
			return nil, err
		}
	default:
		return nil, err
	}

	return &Indexer{
		index: index,
	}, nil
}

type file struct {
	Name          string
	TokenizedName string
}

var filesToIgnore = map[string]bool{
	".Trashes":   true,
	".fseventsd": true,
	".DS_Store":  true,
}

func (i *Indexer) doIndexDir(fs *libfs.FS) error {
	children, err := fs.ReadDir("")
	if err != nil {
		return err
	}

	for _, fi := range children {
		name := fi.Name()
		if filesToIgnore[name] || strings.HasPrefix(name, "._") {
			continue
		}

		tokenized := strings.ReplaceAll(name, "_", " ")
		tokenized = strings.ReplaceAll(tokenized, "-", " ")
		tokenized = strings.ReplaceAll(tokenized, ".", " ")
		f := file{
			Name:          name,
			TokenizedName: tokenized,
		}
		id := fs.Join(fs.Root(), name)
		err := i.index.Index(id, f)
		if err != nil {
			return err
		}

		if fi.IsDir() {
			childFS, err := fs.ChrootAsLibFS(name)
			if err != nil {
				return err
			}
			err = i.doIndexDir(childFS)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (i *Indexer) Index(fs *libfs.FS) error {
	return i.doIndexDir(fs)
}

func (i *Indexer) Search(queryString string) (paths []string, err error) {
	query := bleve.NewQueryStringQuery(queryString)
	request := bleve.NewSearchRequest(query)
	result, err := i.index.Search(request)
	if err != nil {
		return nil, err
	}

	for _, hit := range result.Hits {
		paths = append(paths, hit.ID)
	}
	return paths, nil
}
