// Package spawn 拉起常驻进程：新开会话，并让它自己对系统授权负责。
package spawn

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// 系统按"负责进程"认屏幕录制、桌面文件夹等授权，posix_spawn 默认让子进程继承拉起方的。
// 常驻进程由窗口、终端里的 CLI 或交接时的旧进程拉起，继承下来的可能是终端 App，
// 也可能是早已被换掉的旧 Posse.app（系统认不出它的签名，每次访问都弹授权）。
// responsibility_spawnattrs_setdisclaim（私有接口）让子进程按自己的签名认，
// 常驻进程和它下面的终端就一直记在 Posse.app 上。

const (
	flagSetSigDef      = 0x0004
	flagSetSigMask     = 0x0008
	flagSetSID         = 0x0400
	flagCloexecDefault = 0x4000
)

var (
	loadOnce sync.Once
	loadErr  error

	spawnFn         func(pid *int32, path *byte, actions, attr *unsafe.Pointer, argv, env **byte) int32
	actionsInit     func(actions *unsafe.Pointer) int32
	actionsDestroy  func(actions *unsafe.Pointer) int32
	actionsDup2     func(actions *unsafe.Pointer, fd, newfd int32) int32
	actionsChdir    func(actions *unsafe.Pointer, path *byte) int32
	attrInit        func(attr *unsafe.Pointer) int32
	attrDestroy     func(attr *unsafe.Pointer) int32
	attrSetFlags    func(attr *unsafe.Pointer, flags int16) int32
	attrSetSigMask  func(attr *unsafe.Pointer, set *uint32) int32
	attrSetSigDef   func(attr *unsafe.Pointer, set *uint32) int32
	disclaim        func(attr *unsafe.Pointer, disclaim int32) int32
	disclaimMissing bool
)

func load() error {
	loadOnce.Do(func() {
		lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = err
			return
		}
		for name, fn := range map[string]any{
			"posix_spawn":                          &spawnFn,
			"posix_spawn_file_actions_init":        &actionsInit,
			"posix_spawn_file_actions_destroy":     &actionsDestroy,
			"posix_spawn_file_actions_adddup2":     &actionsDup2,
			"posix_spawn_file_actions_addchdir_np": &actionsChdir,
			"posix_spawnattr_init":                 &attrInit,
			"posix_spawnattr_destroy":              &attrDestroy,
			"posix_spawnattr_setflags":             &attrSetFlags,
			"posix_spawnattr_setsigmask":           &attrSetSigMask,
			"posix_spawnattr_setsigdefault":        &attrSetSigDef,
		} {
			sym, err := purego.Dlsym(lib, name)
			if err != nil {
				loadErr = errors.Join(loadErr, err)
				continue
			}
			purego.RegisterFunc(fn, sym)
		}
		// 私有接口找不到时照常拉起，只是不改授权归属
		if sym, err := purego.Dlsym(lib, "responsibility_spawnattrs_setdisclaim"); err == nil {
			purego.RegisterFunc(&disclaim, sym)
		} else {
			disclaimMissing = true
		}
	})
	return loadErr
}

// Start 按 cmd 的 Path、Args、Env、Dir、Stdin/Stdout/Stderr（只认 *os.File，nil 接 /dev/null）
// 和 ExtraFiles（依次是 3、4……）启动进程，新开会话，信号屏蔽和处理方式恢复默认，其余描述符不继承。
// 返回的进程由调用方 Wait。
func Start(cmd *exec.Cmd) (*os.Process, error) {
	if err := load(); err != nil {
		return nil, fmt.Errorf("spawn: %w", err)
	}
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	args := cmd.Args
	if len(args) == 0 {
		args = []string{cmd.Path}
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}

	var files []*os.File
	for i, s := range []any{cmd.Stdin, cmd.Stdout, cmd.Stderr} {
		switch f := s.(type) {
		case nil:
			mode := os.O_WRONLY
			if i == 0 {
				mode = os.O_RDONLY
			}
			null, err := os.OpenFile(os.DevNull, mode, 0)
			if err != nil {
				return nil, err
			}
			defer null.Close()
			files = append(files, null)
		case *os.File:
			files = append(files, f)
		default:
			return nil, fmt.Errorf("spawn: stdio %d must be *os.File, got %T", i, s)
		}
	}
	files = append(files, cmd.ExtraFiles...)

	// 先把每个来源复制到所有目标编号之上，dup2 时来源不会被前面的目标覆盖；
	// 复制出来的带 CLOEXEC，加上 CLOEXEC_DEFAULT，子进程只留下 dup2 的目标
	srcs := make([]int, len(files))
	for i, f := range files {
		conn, err := f.SyscallConn()
		if err != nil {
			return nil, err
		}
		var dupErr error
		if err := conn.Control(func(fd uintptr) {
			srcs[i], dupErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, len(files))
		}); err != nil {
			return nil, err
		}
		if dupErr != nil {
			for _, fd := range srcs[:i] {
				unix.Close(fd)
			}
			return nil, dupErr
		}
	}
	defer func() {
		for _, fd := range srcs {
			unix.Close(fd)
		}
	}()

	var actions, attr unsafe.Pointer
	if rc := actionsInit(&actions); rc != 0 {
		return nil, fmt.Errorf("spawn: file actions: %w", syscall.Errno(rc))
	}
	defer actionsDestroy(&actions)
	if rc := attrInit(&attr); rc != 0 {
		return nil, fmt.Errorf("spawn: attr: %w", syscall.Errno(rc))
	}
	defer attrDestroy(&attr)

	for i, fd := range srcs {
		if rc := actionsDup2(&actions, int32(fd), int32(i)); rc != 0 {
			return nil, fmt.Errorf("spawn: dup2 %d: %w", i, syscall.Errno(rc))
		}
	}
	var dir []byte
	if cmd.Dir != "" {
		dir = append([]byte(cmd.Dir), 0)
		if rc := actionsChdir(&actions, &dir[0]); rc != 0 {
			return nil, fmt.Errorf("spawn: chdir: %w", syscall.Errno(rc))
		}
	}

	var none, all uint32 = 0, ^uint32(0)
	if rc := attrSetFlags(&attr, flagSetSID|flagCloexecDefault|flagSetSigMask|flagSetSigDef); rc != 0 {
		return nil, fmt.Errorf("spawn: flags: %w", syscall.Errno(rc))
	}
	attrSetSigMask(&attr, &none)
	attrSetSigDef(&attr, &all)
	if !disclaimMissing {
		if rc := disclaim(&attr, 1); rc != 0 {
			return nil, fmt.Errorf("spawn: disclaim: %w", syscall.Errno(rc))
		}
	}

	path := append([]byte(cmd.Path), 0)
	argv, argvKeep := cStrings(args)
	envp, envKeep := cStrings(env)
	var pid int32
	rc := spawnFn(&pid, &path[0], &actions, &attr, &argv[0], &envp[0])
	runtime.KeepAlive(path)
	runtime.KeepAlive(dir)
	runtime.KeepAlive(argvKeep)
	runtime.KeepAlive(envKeep)
	if rc != 0 {
		return nil, fmt.Errorf("spawn %s: %w", cmd.Path, syscall.Errno(rc))
	}
	return os.FindProcess(int(pid))
}

func cStrings(list []string) ([]*byte, [][]byte) {
	keep := make([][]byte, len(list))
	out := make([]*byte, len(list)+1)
	for i, s := range list {
		keep[i] = append([]byte(s), 0)
		out[i] = &keep[i][0]
	}
	return out, keep
}
