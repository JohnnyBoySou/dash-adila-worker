// Package backup implementa backup automático de containers gerenciados para o
// Cloudflare R2. Este arquivo contém a assinatura AWS Sig V4 (pura Go, sem deps
// externos), necessária pois R2 é S3-compatível mas requer a assinatura.
package backup

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	r2Region  = "auto"
	r2Service = "s3"
)

// signRequest assina o *http.Request in-place com AWS Sig V4, adicionando os
// headers Authorization, x-amz-date e x-amz-content-sha256.
// body pode ser nil (equivale a corpo vazio — SHA256("") = e3b0c44...).
func signRequest(req *http.Request, accessKeyID, secretKey string, body []byte) {
	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	bodyHash := sha256Hex(body)

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", bodyHash)
	// host é obrigatório no canonical request; garante que esteja nos headers.
	if req.Header.Get("host") == "" {
		req.Header.Set("host", req.URL.Host)
	}

	signedHeaders, canonHeaders := buildCanonHeaders(req)

	canonReq := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL),
		canonicalQueryString(req.URL),
		canonHeaders,
		signedHeaders,
		bodyHash,
	}, "\n")

	credScope := dateStamp + "/" + r2Region + "/" + r2Service + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + credScope + "\n" + sha256Hex([]byte(canonReq))

	sigKey := deriveSigningKey(secretKey, dateStamp, r2Region, r2Service)
	sig := hex.EncodeToString(hmacSHA256(sigKey, []byte(stringToSign)))

	req.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential="+accessKeyID+"/"+credScope+
			", SignedHeaders="+signedHeaders+
			", Signature="+sig)
}

// buildCanonHeaders constrói o bloco de canonical headers (nome:valor\n) e a
// lista de signed headers (nome;nome) a partir dos headers do request. Só são
// incluídos host e headers x-amz-*.
func buildCanonHeaders(req *http.Request) (signedHeaders, canonHeaders string) {
	headers := make(map[string]string)
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "host" || strings.HasPrefix(lk, "x-amz-") {
			headers[lk] = strings.TrimSpace(strings.Join(v, ","))
		}
	}
	if _, ok := headers["host"]; !ok {
		headers["host"] = req.URL.Host
	}

	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte(':')
		sb.WriteString(headers[k])
		sb.WriteByte('\n')
	}
	return strings.Join(keys, ";"), sb.String()
}

// canonicalURI retorna o caminho URI normalizado. Nunca vazio (mínimo "/").
func canonicalURI(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	return p
}

// canonicalQueryString ordena os parâmetros e usa url.QueryEscape (encodes "/" como
// "%2F", necessário para canonical form do S3).
func canonicalQueryString(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	params, _ := url.ParseQuery(u.RawQuery)
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := params[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func deriveSigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256Hex(data []byte) string {
	if data == nil {
		data = []byte{}
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
