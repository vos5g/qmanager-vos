package daemon

import (
	"strings"
	"testing"
	"time"
)

// AT+BINFO as the stick printed it (cell identity replaced).
const binfoReply = "<LTE PCC INFO>:EARFCN 99,TAC 0\r\nServing Cell ID 0,physical cell ID 31\r\n" +
	"BAND 1,dl_bandwidth 3,ul_bandwidth 3\r\nRX Power Info:\r\nRSRQ -11 dB, RSRP -108 dBm,SINR 9 dB\r\n" +
	"RX0 power: -78 dBm\r\nRX0 rsrp: -105 dBm\r\nRX1 power: -81 dBm\r\nRX1 rsrp: -107 dBm\r\n" +
	"RX2 power: 0 dBm\r\nRX2 rsrp: -256 dBm\r\nRX3 power: 0 dBm\r\nRX3 rsrp: -256 dBm\r\n" +
	"TX POWER INFO:\r\ntx_pwr: 19 dBm,pa_gain_state 0\r\nprach 19,srs 190,pucch 20,pusch160\r\nLTE Modulation:\r\n" +
	"uplink modulation num_layer  1 num_mimo_layers 0 num_config_layers 0\r\nuplink modulation layer 0  16QAM\r\n" +
	"uplink modulation layer 1  BPSK\r\ndownlink modulation num_layer  1 num_mimo_layers 1 num_config_layers 2\r\n" +
	"downlink modulation layer 0  64QAM\r\ndownlink modulation layer 1  BPSK\r\nPCC CQI 12 0\r\nLTE RRC STATE:CONNECTED\r\n\r\nOK\r\n"

func TestParseBINFOLink(t *testing.T) {
	l := parseBINFOLink(binfoReply)
	if l == nil || *l.TxPower != 19 || l.ULModulation != "16QAM" || l.DLModulation != "64QAM" ||
		l.DLLayers != 1 || l.DLConfLayers != 2 || *l.CQI != 12 || l.RxChains != 2 {
		t.Fatalf("got %+v", l)
	}
	if l.RxRSRP[0] == nil || *l.RxRSRP[0] != -105 || l.RxRSRP[2] != nil {
		t.Fatalf("rx rsrp %v", l.RxRSRP)
	}
	if l.mimoLabel() != "LTE 1x2" {
		t.Fatalf("mimo %q", l.mimoLabel())
	}
	if parseBINFOLink("NR service is not available\r\nOK") != nil {
		t.Fatal("non-LTE reply parsed")
	}
	two := strings.Replace(binfoReply, "num_mimo_layers 1", "num_mimo_layers 2", 1)
	if parseBINFOLink(two).mimoLabel() != "LTE 2x2" {
		t.Fatal("2 layers")
	}
}

func TestAddLinkInfo(t *testing.T) {
	linkLast = nil
	p := NewPoller(&scriptAT{replies: map[string]string{"AT+BINFO": binfoReply}}, time.Second)
	st := map[string]interface{}{"lte": map[string]interface{}{}, "device": map[string]interface{}{"mimo": "4x4"}}
	p.addLinkInfo(st, time.Now())
	lte := st["lte"].(map[string]interface{})
	if *lte["tx_power"].(*int) != 19 || lte["ul_modulation"] != "16QAM" || st["device"].(map[string]interface{})["mimo"] != "LTE 1x2" {
		t.Fatalf("got %v %v", lte, st["device"])
	}
}
