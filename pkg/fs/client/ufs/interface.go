/*
Copyright (c) 2021 PaddlePaddle Authors. All Rights Reserve.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ufs

import (
	"fmt"
	"io"
	"time"

	"github.com/PaddlePaddle/PaddleFlow/pkg/fs/client/base"
)

const (
	TypeFile      = 1 // type for regular file
	TypeDirectory = 2 // type for directory
)

// under file storage interface, copy from pathfs.FileSystem,
// and remove api OnMount and OnUnmount
type UnderFileStorage interface {
	// Used for pretty printing.
	String() string

	// Attributes.  This function is the main entry point, through
	// which FUSE discovers which files and directories exist.
	//
	// If the filesystem wants to implement hard-links, it should
	// return consistent non-zero FileInfo.Ino data.  Using
	// hardlinks incurs a performance hit.
	GetAttr(name string) (*base.FileInfo, error)

	// These should update the file's ctime too.
	// Note: raw FUSE setattr is translated into Chmod/Chown/Utimens in the higher level APIs.
	Chmod(name string, mode uint32) error
	Chown(name string, uid uint32, gid uint32) error
	Utimens(name string, Atime *time.Time, Mtime *time.Time) error

	Truncate(name string, size uint64) error

	Access(name string, mode, callerUid, callerGid uint32) error

	// Tree structure
	Link(oldName string, newName string) error
	Mkdir(name string, mode uint32) error
	Mknod(name string, mode uint32, dev uint32) error
	Rename(oldName string, newName string) error
	Rmdir(name string) error
	Unlink(name string) error

	// Extended attributes.
	GetXAttr(name string, attribute string) (data []byte, err error)
	ListXAttr(name string) (attributes []string, err error)
	RemoveXAttr(name string, attr string) error
	SetXAttr(name string, attr string, data []byte, flags int) error

	// File handling.  If opening for writing, the file's mtime
	// should be updated too.
	Open(name string, flags uint32) (fd base.FileHandle, err error)
	Create(name string, flags uint32, mode uint32) (fd base.FileHandle, err error)

	// Directory handling
	ReadDir(name string) (stream []DirEntry, err error)

	// Symlinks.
	Symlink(value string, linkName string) error
	Readlink(name string) (string, error)

	StatFs(name string) *base.StatfsOut

	Get(name string, flags uint32, off, limit int64) (io.ReadCloser, error)

	Put(name string, reader io.Reader) error
}

type withCloser struct {
	io.Reader
	io.Closer
}

type Creator func(properties map[string]interface{}) (UnderFileStorage, error)

type DirEntry struct {
	Attr *Attr
	// Name is the basename of the file in the directory.
	Name string
}

type Ino uint64

// Attr represents attributes of a node.
type Attr struct {
	Type      uint8  // type of a node
	Mode      uint32 // permission mode
	Uid       uint32 // owner id
	Gid       uint32 // group id of owner
	Rdev      uint64 // device number
	Atime     int64  // last access time
	Mtime     int64  // last modified time
	Ctime     int64  // last change time for meta
	Atimensec uint32 // nanosecond part of atime
	Mtimensec uint32 // nanosecond part of mtime
	Ctimensec uint32 // nanosecond part of ctime
	Nlink     uint64 // number of links (sub-directories or hardlinks)
	Size      uint64 // size of regular file
	Blksize   int64  // 目录默认4096 文件为0
	Block     int64  // 文件size的大小/512
}

var ufs = make(map[string]Creator)

func RegisterUFS(_type string, creator Creator) {
	ufs[_type] = creator
}

func NewUFS(_type string, properties map[string]interface{}) (UnderFileStorage, error) {
	fs, ok := ufs[_type]
	if ok {
		return fs(properties)
	}
	return nil, fmt.Errorf("unknow ufs")
}
