package adb

import "testing"

func TestParseDefaultDisplaySize(t *testing.T) {
	for _, test := range []struct {
		name, dump string
		want       DisplaySize
	}{
		{"portrait", "Display: mDisplayId=0 rootTasks=1\n  init=1080x1920 240dpi cur=1080x1920 app=1080x1810", DisplaySize{1080, 1920}},
		{"landscape override", "Display: mDisplayId=0 rootTasks=1\r\n init=1080x1920 base=900x1600 cur=1600x900\r\nDisplay: mDisplayId=1\n init=1x1 cur=1x1", DisplaySize{1600, 900}},
		{"separate current line", "Display: mDisplayId=0\n init=1080x1920 noscale\n cur=1080x1920", DisplaySize{1080, 1920}},
		{"secondary only", "Display: mDisplayId=1\n init=1x1 cur=1x1", DisplaySize{}},
		{"current only on secondary", "Display: mDisplayId=0\n init=1x1\nDisplay: mDisplayId=1\n cur=1x1", DisplaySize{}},
		{"ambiguous", "Display: mDisplayId=0\n cur=1x2 cur=2x1", DisplaySize{}},
		{"invalid dimensions", "Display: mDisplayId=0\n cur=0x900", DisplaySize{}},
		{"overflow", "Display: mDisplayId=0\n cur=999999999999x900", DisplaySize{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseDefaultDisplaySize(test.dump)
			if got != test.want || ok != (test.want.Width != 0) {
				t.Fatalf("size = %+v, valid=%t, want %+v", got, ok, test.want)
			}
		})
	}
}
