package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// 聊天磁贴：一个固定的项目目录，每次在里面启动 agent 都新建一个子文件夹，临时的对话各自放各自的文件。
// 目录存在 agents.json，窗口改、常驻进程每次启动 agent 时读：手机和 CLI 在这个工作区里开的 agent 也一样分文件夹。

// ChatDir 是聊天目录：设置里改过的，否则是 ~/Posse（开发包和测试的 home 也用它）。
func ChatDir(home string) string {
	if d := loadSettings(home).ChatDir; d != "" {
		return d
	}
	return DefaultChatDir()
}

// DefaultChatDir 是 ~/Posse：放在看得见的地方，访达和聚焦能找到聊天里生成的文件；home 根目录不用系统授权。
func DefaultChatDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Posse")
}

// SetChatDir 改聊天目录，dir 为空时恢复默认。
func SetChatDir(home, dir string) error {
	s := loadSettings(home)
	s.ChatDir = dir
	if dir != "" && filepath.Clean(dir) == DefaultChatDir() {
		s.ChatDir = ""
	}
	return saveSettings(home, s)
}

// chatFolderRe 是 NewChatFolder 起的名字；退出时只清理这种名字的空文件夹，不碰用户自己建的。
var chatFolderRe = regexp.MustCompile(`^\d{4}-\d{4}(-\d+)?$`)

// IsChatFolder 判断 dir 是不是 chatDir 下面一次聊天的文件夹。
func IsChatFolder(chatDir, dir string) bool {
	return filepath.Dir(filepath.Clean(dir)) == filepath.Clean(chatDir) && chatFolderRe.MatchString(filepath.Base(dir))
}

// NewChatFolder 在 chatDir 下建一个按「月日-时分」命名的文件夹，同一分钟已有就加序号。
// 名字建好就不改：会话标题出来以后再改名，正在运行的 agent 的工作目录就失效了，Claude 按路径存的会话也接不上。
func NewChatFolder(chatDir string, now time.Time) (string, error) {
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		return "", err
	}
	base := now.Format("0102-1504")
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		dir := filepath.Join(chatDir, name)
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			return dir, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
	}
}

// RemoveChatFolder 在聊天结束后删掉没留下文件的文件夹；os.Remove 只删空目录，有文件就留着。
func RemoveChatFolder(chatDir, dir string) {
	if IsChatFolder(chatDir, dir) {
		os.Remove(dir)
	}
}
