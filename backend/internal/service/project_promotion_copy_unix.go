//go:build darwin || linux

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const projectCopyBufferSize = 32 * 1024

func copyProjectDirectoryStrict(ctx context.Context, source, target string) error {
	return copyProjectDirectoryStrictWithHook(ctx, source, target, nil)
}

func copyProjectDirectoryStrictWithHook(
	ctx context.Context,
	source,
	target string,
	hook func(relativePath string) error,
) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}

	sourceFD, err := unix.Open(source, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open runtime project source directory %s: %w", source, err)
	}
	sourceDir, err := newProjectCopyFile(sourceFD, source)
	if err != nil {
		return err
	}
	defer closeProjectCopyFile(&resultErr, sourceDir, "runtime project source directory")

	var sourceStat unix.Stat_t
	if err := unix.Fstat(sourceFD, &sourceStat); err != nil {
		return fmt.Errorf("inspect opened runtime project source directory %s: %w", source, err)
	}
	if projectCopyFileType(sourceStat.Mode) != unix.S_IFDIR {
		return fmt.Errorf("runtime project source must be a real directory: %s", source)
	}

	directoryMode := projectCopyPermissions(sourceStat.Mode)
	if err := unix.Mkdir(target, directoryMode|0o700); err != nil {
		return fmt.Errorf("create runtime project target directory %s: %w", target, err)
	}
	targetFD, err := unix.Open(target, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open runtime project target directory %s: %w", target, err)
	}
	targetDir, err := newProjectCopyFile(targetFD, target)
	if err != nil {
		return err
	}
	defer closeProjectCopyFile(&resultErr, targetDir, "runtime project target directory")

	if err := copyProjectDirectoryContents(ctx, sourceDir, targetDir, "", hook); err != nil {
		return err
	}
	if err := unix.Fchmod(targetFD, directoryMode); err != nil {
		return fmt.Errorf("preserve runtime project root permissions: %w", err)
	}
	return nil
}

func copyProjectDirectoryContents(
	ctx context.Context,
	sourceDir,
	targetDir *os.File,
	relativeDir string,
	hook func(relativePath string) error,
) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := sourceDir.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := entry.Name()
			relativePath := name
			if relativeDir != "" {
				relativePath = filepath.Join(relativeDir, name)
			}

			var classified unix.Stat_t
			if err := unix.Fstatat(
				int(sourceDir.Fd()),
				name,
				&classified,
				unix.AT_SYMLINK_NOFOLLOW,
			); err != nil {
				return fmt.Errorf("inspect runtime project member %s: %w", relativePath, err)
			}
			fileType := projectCopyFileType(classified.Mode)
			switch fileType {
			case unix.S_IFLNK:
				return fmt.Errorf("runtime project contains symlink: %s", relativePath)
			case unix.S_IFDIR, unix.S_IFREG:
			default:
				return fmt.Errorf("runtime project contains unsupported file type %#o: %s", fileType, relativePath)
			}

			if hook != nil {
				if err := hook(relativePath); err != nil {
					return fmt.Errorf("prepare to open runtime project member %s: %w", relativePath, err)
				}
			}
			sourceChild, err := openClassifiedProjectMember(
				int(sourceDir.Fd()),
				name,
				relativePath,
				fileType,
				&classified,
			)
			if err != nil {
				return err
			}

			switch fileType {
			case unix.S_IFDIR:
				err = copyProjectDirectoryMember(ctx, sourceChild, targetDir, name, relativePath, &classified, hook)
			case unix.S_IFREG:
				err = copyProjectRegularMember(ctx, sourceChild, targetDir, name, relativePath, &classified)
			}
			if err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read runtime project directory %s: %w", projectCopyDisplayPath(relativeDir), readErr)
		}
	}
}

func openClassifiedProjectMember(
	parentFD int,
	name,
	relativePath string,
	fileType uint32,
	classified *unix.Stat_t,
) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if fileType == unix.S_IFDIR {
		flags |= unix.O_DIRECTORY
	} else {
		flags |= unix.O_NONBLOCK
	}
	fd, err := unix.Openat(parentFD, name, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("open runtime project member %s without following links: %w", relativePath, err)
	}
	file, err := newProjectCopyFile(fd, relativePath)
	if err != nil {
		return nil, err
	}

	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return nil, errors.Join(
			fmt.Errorf("inspect opened runtime project member %s: %w", relativePath, err),
			closeProjectCopyFileNow(file, relativePath),
		)
	}
	if projectCopyFileType(opened.Mode) != fileType ||
		opened.Dev != classified.Dev || opened.Ino != classified.Ino {
		return nil, errors.Join(
			fmt.Errorf("runtime project member changed while staging: %s", relativePath),
			closeProjectCopyFileNow(file, relativePath),
		)
	}
	return file, nil
}

func copyProjectDirectoryMember(
	ctx context.Context,
	sourceDir,
	targetParent *os.File,
	name,
	relativePath string,
	classified *unix.Stat_t,
	hook func(relativePath string) error,
) (resultErr error) {
	defer closeProjectCopyFile(&resultErr, sourceDir, relativePath)

	directoryMode := projectCopyPermissions(classified.Mode)
	if err := unix.Mkdirat(int(targetParent.Fd()), name, directoryMode|0o700); err != nil {
		return fmt.Errorf("create runtime project directory %s: %w", relativePath, err)
	}
	targetFD, err := unix.Openat(
		int(targetParent.Fd()),
		name,
		unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return fmt.Errorf("open runtime project target directory %s: %w", relativePath, err)
	}
	targetDir, err := newProjectCopyFile(targetFD, relativePath)
	if err != nil {
		return err
	}
	defer closeProjectCopyFile(&resultErr, targetDir, relativePath)

	if err := copyProjectDirectoryContents(ctx, sourceDir, targetDir, relativePath, hook); err != nil {
		return err
	}
	if err := unix.Fchmod(targetFD, directoryMode); err != nil {
		return fmt.Errorf("preserve runtime project directory permissions for %s: %w", relativePath, err)
	}
	return nil
}

func copyProjectRegularMember(
	ctx context.Context,
	sourceFile,
	targetParent *os.File,
	name,
	relativePath string,
	classified *unix.Stat_t,
) (resultErr error) {
	defer closeProjectCopyFile(&resultErr, sourceFile, relativePath)

	fileMode := projectCopyPermissions(classified.Mode)
	targetFD, err := unix.Openat(
		int(targetParent.Fd()),
		name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		fileMode,
	)
	if err != nil {
		return fmt.Errorf("create runtime project file %s: %w", relativePath, err)
	}
	targetFile, err := newProjectCopyFile(targetFD, relativePath)
	if err != nil {
		return err
	}
	defer closeProjectCopyFile(&resultErr, targetFile, relativePath)

	if err := copyProjectFileContents(ctx, sourceFile, targetFile, relativePath); err != nil {
		return err
	}
	if err := unix.Fchmod(targetFD, fileMode); err != nil {
		return fmt.Errorf("preserve runtime project file permissions for %s: %w", relativePath, err)
	}
	return nil
}

func copyProjectFileContents(ctx context.Context, source, target *os.File, relativePath string) error {
	buffer := make([]byte, projectCopyBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		readCount, readErr := source.Read(buffer)
		written := 0
		for written < readCount {
			if err := ctx.Err(); err != nil {
				return err
			}
			writeCount, writeErr := target.Write(buffer[written:readCount])
			written += writeCount
			if writeErr != nil {
				return fmt.Errorf("write runtime project file %s: %w", relativePath, writeErr)
			}
			if writeCount == 0 {
				return fmt.Errorf("write runtime project file %s: %w", relativePath, io.ErrShortWrite)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("read runtime project file %s: %w", relativePath, readErr)
		}
	}
}

func newProjectCopyFile(fd int, name string) (*os.File, error) {
	file := os.NewFile(uintptr(fd), name)
	if file != nil {
		return file, nil
	}
	return nil, errors.Join(
		fmt.Errorf("manage opened runtime project descriptor for %s", name),
		unix.Close(fd),
	)
}

func closeProjectCopyFile(resultErr *error, file *os.File, name string) {
	*resultErr = errors.Join(*resultErr, closeProjectCopyFileNow(file, name))
}

func closeProjectCopyFileNow(file *os.File, name string) error {
	if err := file.Close(); err != nil {
		return fmt.Errorf("close runtime project descriptor for %s: %w", name, err)
	}
	return nil
}

func projectCopyFileType[T ~uint16 | ~uint32](mode T) uint32 {
	return uint32(mode) & uint32(unix.S_IFMT)
}

func projectCopyPermissions[T ~uint16 | ~uint32](mode T) uint32 {
	return uint32(mode) & 0o777
}

func projectCopyDisplayPath(relativePath string) string {
	if relativePath == "" {
		return "."
	}
	return relativePath
}
