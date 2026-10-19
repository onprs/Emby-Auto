package httpapi

import "testing"

func TestParseByteRangeBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		size      int64
		wantStart int64
		wantEnd   int64
		wantError bool
	}{
		{name: "单字节", header: "bytes=0-0", size: 10, wantStart: 0, wantEnd: 0},
		{name: "开放结束位置", header: "bytes=7-", size: 10, wantStart: 7, wantEnd: 9},
		{name: "后缀范围", header: "bytes=-3", size: 10, wantStart: 7, wantEnd: 9},
		{name: "超长后缀", header: "bytes=-30", size: 10, wantStart: 0, wantEnd: 9},
		{name: "结束位置截断", header: "bytes=7-30", size: 10, wantStart: 7, wantEnd: 9},
		{name: "多个范围", header: "bytes=0-1,4-5", size: 10, wantError: true},
		{name: "首位置正号", header: "bytes=+1-3", size: 10, wantError: true},
		{name: "结束位置正号", header: "bytes=1-+3", size: 10, wantError: true},
		{name: "后缀正号", header: "bytes=-+3", size: 10, wantError: true},
		{name: "空资源后缀", header: "bytes=-3", size: 0, wantError: true},
		{name: "零长度后缀", header: "bytes=-0", size: 10, wantError: true},
		{name: "起点越界", header: "bytes=10-", size: 10, wantError: true},
		{name: "逆序范围", header: "bytes=5-3", size: 10, wantError: true},
		{name: "数字溢出", header: "bytes=9223372036854775808-", size: 10, wantError: true},
		{name: "缺少范围", header: "bytes=-", size: 10, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start, end, ok := parseByteRange(test.header, test.size)
			if ok == test.wantError {
				t.Fatalf("parseByteRange(%q, %d) = %d, %d, %t", test.header, test.size, start, end, ok)
			}
			if !test.wantError && (start != test.wantStart || end != test.wantEnd) {
				t.Fatalf("范围为 %d-%d，预期 %d-%d", start, end, test.wantStart, test.wantEnd)
			}
		})
	}
}
