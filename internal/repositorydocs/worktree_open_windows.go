//go:build windows

package repositorydocs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func readWorktreeFileNoFollow(ctx context.Context, root, repoPath string, maxBytes int64) ([]byte, int64, error) {
	return readWorktreeFileNoFollowAfterOpen(ctx, root, repoPath, maxBytes, nil)
}

// Windows has no openat(2). Holding every verified directory handle without
// FILE_SHARE_DELETE prevents ancestor replacement while the next component is
// opened. FILE_FLAG_OPEN_REPARSE_POINT then rejects junctions and symlinks at
// every component, including the final file.
func readWorktreeFileNoFollowAfterOpen(ctx context.Context, root, repoPath string, maxBytes int64, afterOpen func()) ([]byte, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	parts := strings.Split(repoPath, "/")
	if len(parts) == 0 {
		return nil, 0, os.ErrInvalid
	}
	handles := make([]windows.Handle, 0, len(parts))
	defer func() {
		for _, handle := range handles {
			_ = windows.CloseHandle(handle)
		}
	}()
	current := root
	rootHandle, err := openWindowsNoReparse(current, true)
	if err != nil {
		return nil, 0, err
	}
	handles = append(handles, rootHandle)
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			return nil, 0, os.ErrInvalid
		}
		current = filepath.Join(current, part)
		handle, err := openWindowsNoReparse(current, true)
		if err != nil {
			return nil, 0, err
		}
		handles = append(handles, handle)
	}
	name := parts[len(parts)-1]
	if name == "" || name == "." || name == ".." {
		return nil, 0, os.ErrInvalid
	}
	current = filepath.Join(current, name)
	path, err := windows.UTF16PtrFromString(current)
	if err != nil {
		return nil, 0, err
	}
	fileHandle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, 0, err
	}
	var fileInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(fileHandle, &fileInfo); err != nil || fileInfo.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = windows.CloseHandle(fileHandle)
		return nil, 0, os.ErrInvalid
	}
	file := os.NewFile(uintptr(fileHandle), "repository-document")
	if file == nil {
		_ = windows.CloseHandle(fileHandle)
		return nil, 0, os.ErrInvalid
	}
	defer file.Close()
	if afterOpen != nil {
		afterOpen()
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, os.ErrInvalid
	}
	actualBytes := info.Size()
	if actualBytes > maxBytes {
		return nil, actualBytes, ErrDocumentTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, actualBytes, err
	}
	if int64(len(data)) > maxBytes {
		return nil, int64(len(data)), ErrDocumentTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, actualBytes, err
	}
	return data, actualBytes, nil
}

func openWindowsNoReparse(pathValue string, directory bool) (windows.Handle, error) {
	path, err := windows.UTF16PtrFromString(pathValue)
	if err != nil {
		return windows.InvalidHandle, err
	}
	access := uint32(windows.FILE_READ_ATTRIBUTES)
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		access |= windows.FILE_LIST_DIRECTORY
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(path, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return windows.InvalidHandle, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || directory && info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, os.ErrInvalid
	}
	return handle, nil
}
