package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
)

func probeCredentialCipher() (cipher.AEAD, error) {
	key, err := base64.StdEncoding.DecodeString(os.Getenv("PROBE_CREDENTIAL_KEY"))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("请配置固定的 PROBE_CREDENTIAL_KEY（32 字节 base64），用于加密保存探针密钥")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encryptProbeToken(token string) ([]byte, error) {
	aead, err := probeCredentialCipher()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, []byte(token), []byte("ip-list/probe-token/v1")), nil
}

func decryptProbeToken(encrypted []byte) (string, error) {
	if len(encrypted) == 0 {
		return "", nil
	}
	aead, err := probeCredentialCipher()
	if err != nil {
		return "", err
	}
	if len(encrypted) < aead.NonceSize() {
		return "", errors.New("探针密钥密文无效")
	}
	plain, err := aead.Open(nil, encrypted[:aead.NonceSize()], encrypted[aead.NonceSize():], []byte("ip-list/probe-token/v1"))
	return string(plain), err
}

func (a *app) probeDeployment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	if validate.Var(id, "uuid") != nil {
		apiError(w, 400, "探针 ID 无效", nil)
		return
	}
	if r.Method == http.MethodPut {
		var input struct {
			Token    string `json:"token"`
			Platform string `json:"platform"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Platform != "darwin" && input.Platform != "linux" && input.Platform != "windows" {
			apiError(w, 422, "请选择有效系统", nil)
			return
		}
		var encrypted []byte
		var err error
		if input.Token != "" {
			encrypted, err = encryptProbeToken(input.Token)
			if err != nil {
				apiError(w, 503, err.Error(), nil)
				return
			}
		}
		result, err := a.db.Exec(r.Context(), `UPDATE latency_probes SET deployment_platform=$2,
 token_encrypted=CASE WHEN $3::bytea IS NULL THEN token_encrypted ELSE $3 END
 WHERE id=$1 AND ($3::bytea IS NULL OR token_hash=$4)`, id, input.Platform, encrypted, probeTokenHash(input.Token))
		if err != nil {
			dbError(w, err)
			return
		}
		if result.RowsAffected() == 0 {
			apiError(w, 422, "探针不存在或填写的密钥与当前密钥不符；保存不会更换密钥", nil)
			return
		}
	}
	var encrypted []byte
	var platform string
	err := a.db.QueryRow(r.Context(), "SELECT token_encrypted,deployment_platform FROM latency_probes WHERE id=$1", id).Scan(&encrypted, &platform)
	if err != nil {
		dbError(w, err)
		return
	}
	token, err := decryptProbeToken(encrypted)
	if err != nil {
		apiError(w, 503, "部署密钥解密失败，请检查服务器加密配置", nil)
		return
	}
	respond(w, 200, map[string]string{"token": token, "platform": platform})
}
