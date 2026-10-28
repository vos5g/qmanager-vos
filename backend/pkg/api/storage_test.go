package api

import "testing"

func TestStorageMountsVOS(t *testing.T) {
	m := storageMounts([]byte("ubi0:rootfs / ubifs ro,relatime 0 0\nubi0:usrfs /data ubifs rw,relatime 0 0\nubi1:modem /firmware ubifs ro 0 0\n"))
	if m["/data"].Filesystem != "ubi0:usrfs" || m["/data"].ReadOnly || !m["/"].ReadOnly {
		t.Fatalf("wrong VOS mount metadata: %#v", m)
	}
	if _, ok := m["/usrdata"]; ok {
		t.Fatal("invented nonexistent user-data mount")
	}
}
