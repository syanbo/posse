package store

import "database/sql"

// AddMember 是测试里直接加成员（不经请求 ID 去重）。界面和手机加成员走 AddMemberOnce。
func (s *Store) AddMember(m Member) error {
	return s.withTx(func(tx *sql.Tx) error { return addMember(tx, m, "") })
}
