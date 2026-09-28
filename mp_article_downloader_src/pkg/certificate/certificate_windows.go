//go:build windows

package certificate

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func fetchCertificates() ([]Certificate, error) {
	store, err := openCurrentUserRoot()
	if err != nil {
		return nil, err
	}
	defer windows.CertCloseStore(store, 0)
	var certificates []Certificate
	var previous *windows.CertContext
	for {
		context, err := windows.CertEnumCertificatesInStore(store, previous)
		if err != nil {
			if errors.Is(err, syscall.Errno(0x80092004)) { // CRYPT_E_NOT_FOUND
				break
			}
			return nil, fmt.Errorf("枚举根证书失败: %w", err)
		}
		if context == nil {
			break
		}
		previous = context // CertEnumCertificatesInStore frees the previous context.
		if context.EncodedCert == nil {
			continue
		}
		parsed, err := x509.ParseCertificate(unsafe.Slice(context.EncodedCert, int(context.Length)))
		if err != nil {
			continue
		}
		fingerprint := sha1.Sum(parsed.Raw)
		certificates = append(certificates, Certificate{
			Thumbprint: strings.ToUpper(hex.EncodeToString(fingerprint[:])),
			Subject: CertificateSubject{
				CN: parsed.Subject.CommonName,
				O:  firstSubjectValue(parsed.Subject.Organization),
				OU: firstSubjectValue(parsed.Subject.OrganizationalUnit),
				L:  firstSubjectValue(parsed.Subject.Locality),
				S:  firstSubjectValue(parsed.Subject.Province),
				C:  firstSubjectValue(parsed.Subject.Country),
			},
		})
	}
	return certificates, nil
}

func openCurrentUserRoot() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString("Root")
	if err != nil {
		return 0, err
	}
	store, err := windows.CertOpenStore(uintptr(windows.CERT_STORE_PROV_SYSTEM_REGISTRY), 0, 0,
		windows.CERT_SYSTEM_STORE_CURRENT_USER, uintptr(unsafe.Pointer(name)))
	if err != nil {
		return 0, fmt.Errorf("打开当前用户的根证书列表失败: %w", err)
	}
	return store, nil
}

func firstSubjectValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func installCertificate(cert_data []byte) error {
	block, _ := pem.Decode(cert_data)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Bytes) == 0 {
		return errors.New("证书格式无效")
	}
	store, err := openCurrentUserRoot()
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(store, 0)
	context, err := windows.CertCreateCertificateContext(windows.X509_ASN_ENCODING, &block.Bytes[0], uint32(len(block.Bytes)))
	if err != nil {
		return fmt.Errorf("读取证书失败: %w", err)
	}
	defer windows.CertFreeCertificateContext(context)
	if err := windows.CertAddCertificateContextToStore(store, context, windows.CERT_STORE_ADD_REPLACE_EXISTING, nil); err != nil {
		return fmt.Errorf("将证书加入当前用户的信任列表失败: %w", err)
	}
	return nil
}

func uninstallCertificate(name string) error {
	store, err := openCurrentUserRoot()
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(store, 0)
	var previous *windows.CertContext
	for {
		context, err := windows.CertEnumCertificatesInStore(store, previous)
		if err != nil {
			if errors.Is(err, syscall.Errno(0x80092004)) {
				return errors.New("没有找到要删除的证书")
			}
			return fmt.Errorf("枚举根证书失败: %w", err)
		}
		if context == nil {
			return errors.New("没有找到要删除的证书")
		}
		previous = context
		if context.EncodedCert == nil {
			continue
		}
		parsed, err := x509.ParseCertificate(unsafe.Slice(context.EncodedCert, int(context.Length)))
		if err == nil && parsed.Subject.CommonName == name {
			return windows.CertDeleteCertificateFromStore(context)
		}
	}
}
