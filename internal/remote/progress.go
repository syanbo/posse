package remote

// Stage 表示 SSH 建连与自动推送无头服务端所处的当前阶段。
type Stage string

const (
	StageAuth      Stage = "auth"
	StageProbe     Stage = "probe"
	StagePackage   Stage = "package"
	StageUpload    Stage = "upload"
	StageStart     Stage = "start"
	StageUpgrade   Stage = "upgrade"
	StageHandshake Stage = "handshake"
)

func (opts Options) report(stage Stage, pct float64) {
	if opts.Progress != nil {
		opts.Progress(stage, pct)
	}
}
