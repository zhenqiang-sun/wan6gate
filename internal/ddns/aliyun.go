package ddns

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	aliyunEndpoint = "https://alidns.aliyuncs.com/"
	aliyunVersion  = "2015-01-09"
	apiTimeout     = 10 * time.Second
)

// AliyunClient is a minimal Aliyun DNS RPC v1 client (HMAC-SHA1 signed).
// We deliberately don't pull in the full SDK — would balloon the binary.
type AliyunClient struct {
	accessKeyID     string
	accessKeySecret string
	httpClient      *http.Client
}

func NewAliyunClient(id, secret string) *AliyunClient {
	return &AliyunClient{
		accessKeyID:     id,
		accessKeySecret: secret,
		httpClient:      &http.Client{Timeout: apiTimeout},
	}
}

// DomainRecord is a normalised view of an Aliyun DNS record.
type DomainRecord struct {
	RecordID string `json:"record_id"`
	RR       string `json:"rr"`
	Type     string `json:"type"`
	Value    string `json:"value"`
	TTL      int    `json:"ttl"`
}

// DescribeDomainRecords looks up records for a zone, optionally filtered by RR/type.
// We filter the response client-side to exact match because Aliyun's RR/Type
// filters use prefix-style matching.
func (c *AliyunClient) DescribeDomainRecords(zone, rr, recordType string) ([]DomainRecord, error) {
	resp, err := c.call("DescribeDomainRecords", map[string]string{
		"DomainName":  zone,
		"RRKeyWord":   rr,
		"TypeKeyWord": recordType,
		"PageSize":    "100",
	})
	if err != nil {
		return nil, err
	}
	var out []DomainRecord
	wrap, _ := resp["DomainRecords"].(map[string]interface{})
	if wrap == nil {
		return nil, errors.New("aliyun: missing DomainRecords in response")
	}
	arr, _ := wrap["Record"].([]interface{})
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		r := DomainRecord{
			RecordID: getStr(m, "RecordId"),
			RR:       getStr(m, "RR"),
			Type:     getStr(m, "Type"),
			Value:    getStr(m, "Value"),
			TTL:      int(getNum(m, "TTL")),
		}
		if rr != "" && r.RR != rr {
			continue
		}
		if recordType != "" && r.Type != recordType {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// AddDomainRecord creates a new record. Returns RecordId on success.
func (c *AliyunClient) AddDomainRecord(zone, rr, recordType, value string, ttl int) (string, error) {
	resp, err := c.call("AddDomainRecord", map[string]string{
		"DomainName": zone,
		"RR":         rr,
		"Type":       recordType,
		"Value":      value,
		"TTL":        strconv.Itoa(ttl),
	})
	if err != nil {
		return "", err
	}
	id := getStr(resp, "RecordId")
	if id == "" {
		return "", errors.New("aliyun: AddDomainRecord returned empty RecordId")
	}
	return id, nil
}

// UpdateDomainRecord updates an existing record by RecordId.
// If the new value is identical, Aliyun returns DomainRecordDuplicate — we
// treat that as success (idempotent).
func (c *AliyunClient) UpdateDomainRecord(recordID, rr, recordType, value string, ttl int) error {
	_, err := c.call("UpdateDomainRecord", map[string]string{
		"RecordId": recordID,
		"RR":       rr,
		"Type":     recordType,
		"Value":    value,
		"TTL":      strconv.Itoa(ttl),
	})
	if err != nil && strings.Contains(err.Error(), "DomainRecordDuplicate") {
		return nil
	}
	return err
}

// call performs a signed RPC GET against Aliyun and returns the parsed JSON map.
// On API-level error responses (4xx/5xx with Code/Message), returns an error
// like `aliyun: Code: Message` while still returning the parsed body.
func (c *AliyunClient) call(action string, params map[string]string) (map[string]interface{}, error) {
	p := make(map[string]string, len(params)+8)
	for k, v := range params {
		p[k] = v
	}
	p["Action"] = action
	p["Format"] = "JSON"
	p["Version"] = aliyunVersion
	p["AccessKeyId"] = c.accessKeyID
	p["SignatureMethod"] = "HMAC-SHA1"
	p["SignatureNonce"] = randNonce()
	p["SignatureVersion"] = "1.0"
	p["Timestamp"] = time.Now().UTC().Format("2006-01-02T15:04:05Z")

	p["Signature"] = c.sign(p)

	q := buildQuery(p)
	req, err := http.NewRequest("GET", aliyunEndpoint+"?"+q, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("aliyun: parse JSON (status %d): %s", resp.StatusCode, truncate(string(body), 300))
	}
	if resp.StatusCode != 200 {
		code, _ := data["Code"].(string)
		msg, _ := data["Message"].(string)
		return data, fmt.Errorf("aliyun: %s: %s", code, msg)
	}
	return data, nil
}

// sign computes the HMAC-SHA1 signature for an Aliyun RPC v1 request.
// Excludes the "Signature" key itself.
func (c *AliyunClient) sign(p map[string]string) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		if k == "Signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, popEncode(k)+"="+popEncode(p[k]))
	}
	canonical := strings.Join(parts, "&")
	stringToSign := "GET&" + popEncode("/") + "&" + popEncode(canonical)

	mac := hmac.New(sha1.New, []byte(c.accessKeySecret+"&"))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// buildQuery serializes params (including Signature) into a URL-encoded query string.
func buildQuery(p map[string]string) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, popEncode(k)+"="+popEncode(p[k]))
	}
	return strings.Join(parts, "&")
}

// popEncode is Aliyun's "POP" URL-encoding variant of RFC 3986.
// It differs from Go's url.QueryEscape in three places: spaces use %20, *
// is encoded, and ~ is left as-is.
func popEncode(s string) string {
	s = url.QueryEscape(s)
	s = strings.ReplaceAll(s, "+", "%20")
	s = strings.ReplaceAll(s, "*", "%2A")
	s = strings.ReplaceAll(s, "%7E", "~")
	return s
}

func randNonce() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return strconv.FormatInt(time.Now().UnixNano(), 10) + hex.EncodeToString(buf)
}

func getStr(m map[string]interface{}, k string) string {
	v, _ := m[k].(string)
	return v
}

func getNum(m map[string]interface{}, k string) float64 {
	v, _ := m[k].(float64)
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
