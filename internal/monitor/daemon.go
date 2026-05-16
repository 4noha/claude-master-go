package monitor

import (
	"os"
	"os/exec"
	"syscall"
)

// newDetached は自バイナリを新セッション（setsid）で背後起動する
// （Python subprocess.Popen(..., start_new_session=True,
// stdout=LOG_FILE, stderr=STDOUT, stdin=DEVNULL) 相当）。親が死んでも
// 生存し、端末シグナルを受けない独立デーモンになる。
func newDetached(self string, args []string, logw *os.File) *exec.Cmd {
	cmd := exec.Command(self, args...)
	if devnull, err := os.Open(os.DevNull); err == nil {
		cmd.Stdin = devnull
	}
	cmd.Stdout = logw
	cmd.Stderr = logw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}
