package platform

import "testing"

// AwakeState 的 Go 侧跟踪与 OS 无关（Linux 上 assert 是 stub 也照样记 active/keep/expires），
// 故可在此自证：SetAwake → active+keepScreen+到期；CancelAwake → 归零；permanent → 无到期。
func TestAwakeStateTracking(t *testing.T) {
	defer CancelAwake()
	if a, _, _ := AwakeState(); a {
		t.Fatal("初始应保持唤醒未激活")
	}

	min := 30
	SetAwake(&min, true)
	active, keep, exp := AwakeState()
	if !active || !keep || exp == nil {
		t.Errorf("SetAwake(30,true) 后应 active+keep+有到期：%v %v %v", active, keep, exp)
	}

	CancelAwake()
	if a, _, e := AwakeState(); a || e != nil {
		t.Errorf("CancelAwake 后应 inactive 且无到期：%v %v", a, e)
	}

	SetAwake(nil, false) // 永久
	if a, keep, e := AwakeState(); !a || keep || e != nil {
		t.Errorf("SetAwake(permanent) 后应 active+不连屏+无到期：%v %v %v", a, keep, e)
	}
}
