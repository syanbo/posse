package rpc

// DesktopOperationCancel 的 Desktop 是当前控制者，Operation 保留原操作接受时的身份。
// 接管后可以取消旧操作，但不能拿旧控制资格请求取消。
type DesktopOperationCancel struct {
	Desktop   DesktopIdentity     `json:"desktop"`
	Operation DesktopOperationRef `json:"operation"`
}

func ValidDesktopOperationRef(ref DesktopOperationRef) bool {
	return ref.Sequence != 0 && ref.Desktop.ClientID != "" && len(ref.Desktop.ClientID) <= 128 && ref.Desktop.InstanceID != "" && ref.Desktop.Epoch != 0
}
