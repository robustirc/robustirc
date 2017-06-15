package proto

import (
	"reflect"
	"testing"

	"github.com/hashicorp/raft"
)

var (
	typeMapping = map[string]string{
		"[]*raft.Log":  "[]*proto.RaftLog",
		"raft.LogType": "proto.RaftLog_LogType",
	}
)

// assertStructsHaveSameFields takes an expression which represents a protocol
// buffer message and an expression which represents a raft message and fails
// the test if the raft message has any fields which are not present in the
// protocol buffer message.
func assertStructsHaveSameFields(t *testing.T, protoExpr, raftExpr interface{}) {
	protoFields := make(map[string]reflect.StructField)

	pst := reflect.TypeOf(protoExpr)
	for i := 0; i < pst.NumField(); i++ {
		f := pst.Field(i)
		protoFields[f.Name] = f
	}

	st := reflect.TypeOf(raftExpr)
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		// Skip unexported fields.
		if f.PkgPath != "" {
			continue
		}
		pf, ok := protoFields[f.Name]
		if !ok {
			t.Errorf("%s's field %q does not have a corresponding field in %s", st, f.Name, pst)
		} else if pf.Type != f.Type && typeMapping[f.Type.String()] != pf.Type.String() {
			t.Errorf("%s's %q field has type %q, %s's has type %q", st, f.Name, f.Type, pst, pf.Type)
		}
	}
}

func TestMessageCompleteness(t *testing.T) {
	assertStructsHaveSameFields(t, RaftLog{}, raft.Log{})
}
