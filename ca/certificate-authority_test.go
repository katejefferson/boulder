// Copyright 2014 ISRG.  All rights reserved
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package ca

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/asn1"
	"fmt"
	"io/ioutil"
	"sort"
	"testing"
	"time"

	cfsslConfig "github.com/letsencrypt/boulder/Godeps/_workspace/src/github.com/cloudflare/cfssl/config"
	"github.com/letsencrypt/boulder/Godeps/_workspace/src/github.com/cloudflare/cfssl/helpers"
	ocspConfig "github.com/letsencrypt/boulder/Godeps/_workspace/src/github.com/cloudflare/cfssl/ocsp/config"
	"github.com/letsencrypt/boulder/Godeps/_workspace/src/github.com/jmhodges/clock"

	"github.com/letsencrypt/boulder/cmd"
	"github.com/letsencrypt/boulder/core"
	"github.com/letsencrypt/boulder/mocks"
	"github.com/letsencrypt/boulder/policy"
	"github.com/letsencrypt/boulder/sa"
	"github.com/letsencrypt/boulder/sa/satest"
	"github.com/letsencrypt/boulder/test"
	"github.com/letsencrypt/boulder/test/vars"
)

var (
	CAkeyPEM  = mustRead("./testdata/ca_key.pem")
	CAcertPEM = mustRead("./testdata/ca_cert.pem")

	// CSR generated by Go:
	// * Random public key
	// * CN = not-example.com
	// * DNSNames = not-example.com, www.not-example.com
	CNandSANCSR = mustRead("./testdata/cn_and_san.der.csr")

	// CSR generated by Go:
	// * Random public key
	// * CN = not-example.com
	// * DNSNames = [none]
	NoSANCSR = mustRead("./testdata/no_san.der.csr")

	// CSR generated by Go:
	// * Random public key
	// * C = US
	// * CN = [none]
	// * DNSNames = not-example.com
	NoCNCSR = mustRead("./testdata/no_cn.der.csr")

	// CSR generated by Go:
	// * Random public key
	// * C = US
	// * CN = [none]
	// * DNSNames = [none]
	NoNameCSR = mustRead("./testdata/no_name.der.csr")

	// CSR generated by Go:
	// * Random public key
	// * CN = [none]
	// * DNSNames = a.example.com, a.example.com
	DupeNameCSR = mustRead("./testdata/dupe_name.der.csr")

	// CSR generated by Go:
	// * Random public key
	// * CN = [none]
	// * DNSNames = not-example.com, www.not-example.com, mail.example.com
	TooManyNameCSR = mustRead("./testdata/too_many_names.der.csr")

	// CSR generated by Go:
	// * Random public key -- 512 bits long
	// * CN = (none)
	// * DNSNames = not-example.com, www.not-example.com, mail.not-example.com
	ShortKeyCSR = mustRead("./testdata/short_key.der.csr")

	// CSR generated by Go:
	// * Random public key
	// * CN = (none)
	// * DNSNames = not-example.com, www.not-example.com, mail.not-example.com
	// * Signature algorithm: SHA1WithRSA
	BadAlgorithmCSR = mustRead("./testdata/bad_algorithm.der.csr")

	// CSR generated by Go:
	// * Random public key
	// * CN = CapiTalizedLetters.com
	// * DNSNames = moreCAPs.com, morecaps.com, evenMOREcaps.com, Capitalizedletters.COM
	CapitalizedCSR = mustRead("./testdata/capitalized_cn_and_san.der.csr")

	// CSR generated by OpenSSL:
	// Edited signature to become invalid.
	WrongSignatureCSR = mustRead("./testdata/invalid_signature.der.csr")

	// CSR generated by Go:
	// * Random public key
	// * CN = not-example.com
	// * Includes an extensionRequest attribute for a well-formed TLS Feature extension
	MustStapleCSR = mustRead("./testdata/must_staple.der.csr")

	// CSR generated by Go:
	// * Random public key
	// * CN = not-example.com
	// * Includes extensionRequest attributes for *two* must-staple extensions
	DuplicateMustStapleCSR = mustRead("./testdata/duplicate_must_staple.der.csr")

	// CSR generated by Go:
	// * Random public key
	// * CN = not-example.com
	// * Includes an extensionRequest attribute for an empty TLS Feature extension
	TLSFeatureUnknownCSR = mustRead("./testdata/tls_feature_unknown.der.csr")

	// CSR generated by Go:
	// * Random public key
	// * CN = not-example.com
	// * Includes an extensionRequest attribute for the CT Poison extension (not supported)
	UnsupportedExtensionCSR = mustRead("./testdata/unsupported_extension.der.csr")

	// CSR generated by Go:
	// * Random ECDSA public key.
	// * CN = [none]
	// * DNSNames = example.com, example2.com
	ECDSACSR = mustRead("./testdata/ecdsa.der.csr")

	// CSR generated by Go:
	// * Random RSA public key.
	// * CN = [none]
	// * DNSNames = [none]
	NoNamesCSR = mustRead("./testdata/no_names.der.csr")

	// CSR generated by Go:
	// * Random RSA public key.
	// * CN = aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.com
	// * DNSNames = [none]
	LongCNCSR = mustRead("./testdata/long_cn.der.csr")

	log = mocks.UseMockLog()
)

// CFSSL config
const rsaProfileName = "rsaEE"
const ecdsaProfileName = "ecdsaEE"
const caKeyFile = "../test/test-ca.key"
const caCertFile = "../test/test-ca.pem"

func mustRead(path string) []byte {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("unable to read %#v: %s", path, err))
	}
	return b
}

type testCtx struct {
	sa        core.StorageAuthority
	caConfig  cmd.CAConfig
	reg       core.Registration
	pa        core.PolicyAuthority
	issuers   []Issuer
	keyPolicy core.KeyPolicy
	fc        clock.FakeClock
	stats     *mocks.Statter
	cleanUp   func()
}

var caKey crypto.Signer
var caCert *x509.Certificate

func init() {
	var err error
	caKey, err = helpers.ParsePrivateKeyPEM(mustRead(caKeyFile))
	if err != nil {
		panic(fmt.Sprintf("Unable to parse %s: %s", caKeyFile, err))
	}
	caCert, err = core.LoadCert(caCertFile)
	if err != nil {
		panic(fmt.Sprintf("Unable to parse %s: %s", caCertFile, err))
	}
}

func setup(t *testing.T) *testCtx {
	// Create an SA
	dbMap, err := sa.NewDbMap(vars.DBConnSA)
	if err != nil {
		t.Fatalf("Failed to create dbMap: %s", err)
	}
	fc := clock.NewFake()
	fc.Add(1 * time.Hour)
	ssa, err := sa.NewSQLStorageAuthority(dbMap, fc)
	if err != nil {
		t.Fatalf("Failed to create SA: %s", err)
	}
	saDBCleanUp := test.ResetSATestDatabase(t)

	paDbMap, err := sa.NewDbMap(vars.DBConnPolicy)
	test.AssertNotError(t, err, "Could not construct dbMap")
	pa, err := policy.New(paDbMap, false, nil)
	test.AssertNotError(t, err, "Couldn't create PADB")
	paDBCleanUp := test.ResetPolicyTestDatabase(t)

	cleanUp := func() {
		saDBCleanUp()
		paDBCleanUp()
	}

	// TODO(jmhodges): use of this pkg here is a bug caused by using a real SA
	reg := satest.CreateWorkingRegistration(t, ssa)

	// Create a CA
	caConfig := cmd.CAConfig{
		RSAProfile:   rsaProfileName,
		ECDSAProfile: ecdsaProfileName,
		SerialPrefix: 17,
		Expiry:       "8760h",
		LifespanOCSP: cmd.ConfigDuration{45 * time.Minute},
		MaxNames:     2,
		DoNotForceCN: true,
		CFSSL: cfsslConfig.Config{
			Signing: &cfsslConfig.Signing{
				Profiles: map[string]*cfsslConfig.SigningProfile{
					rsaProfileName: {
						Usage:     []string{"digital signature", "key encipherment", "server auth"},
						CA:        false,
						IssuerURL: []string{"http://not-example.com/issuer-url"},
						OCSP:      "http://not-example.com/ocsp",
						CRL:       "http://not-example.com/crl",

						Policies: []cfsslConfig.CertificatePolicy{
							{
								ID: cfsslConfig.OID(asn1.ObjectIdentifier{2, 23, 140, 1, 2, 1}),
							},
						},
						ExpiryString: "8760h",
						Backdate:     time.Hour,
						CSRWhitelist: &cfsslConfig.CSRWhitelist{
							PublicKeyAlgorithm: true,
							PublicKey:          true,
							SignatureAlgorithm: true,
						},
						ClientProvidesSerialNumbers: true,
						AllowedExtensions: []cfsslConfig.OID{
							cfsslConfig.OID(oidTLSFeature),
						},
					},
					ecdsaProfileName: {
						Usage:     []string{"digital signature", "server auth"},
						CA:        false,
						IssuerURL: []string{"http://not-example.com/issuer-url"},
						OCSP:      "http://not-example.com/ocsp",
						CRL:       "http://not-example.com/crl",

						Policies: []cfsslConfig.CertificatePolicy{
							{
								ID: cfsslConfig.OID(asn1.ObjectIdentifier{2, 23, 140, 1, 2, 1}),
							},
						},
						ExpiryString: "8760h",
						Backdate:     time.Hour,
						CSRWhitelist: &cfsslConfig.CSRWhitelist{
							PublicKeyAlgorithm: true,
							PublicKey:          true,
							SignatureAlgorithm: true,
						},
						ClientProvidesSerialNumbers: true,
					},
				},
				Default: &cfsslConfig.SigningProfile{
					ExpiryString: "8760h",
				},
			},
			OCSP: &ocspConfig.Config{
				CACertFile:        caCertFile,
				ResponderCertFile: caCertFile,
				KeyFile:           caKeyFile,
			},
		},
	}

	stats := mocks.NewStatter()

	issuers := []Issuer{{caKey, caCert}}

	keyPolicy := core.KeyPolicy{
		AllowRSA:           true,
		AllowECDSANISTP256: true,
		AllowECDSANISTP384: true,
	}

	return &testCtx{
		ssa,
		caConfig,
		reg,
		pa,
		issuers,
		keyPolicy,
		fc,
		&stats,
		cleanUp,
	}
}

func TestFailNoSerial(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()

	ctx.caConfig.SerialPrefix = 0
	_, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	test.AssertError(t, err, "CA should have failed with no SerialPrefix")
}

func TestIssueCertificate(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ca, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	test.AssertNotError(t, err, "Failed to create CA")
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	/*
		  // Uncomment to test with a local signer
			signer, _ := local.NewSigner(caKey, caCert, x509.SHA256WithRSA, nil)
			ca := CertificateAuthorityImpl{
				Signer: signer,
				SA:     sa,
			}
	*/

	csrs := [][]byte{CNandSANCSR, NoSANCSR}
	for _, csrDER := range csrs {
		csr, _ := x509.ParseCertificateRequest(csrDER)

		// Sign CSR
		issuedCert, err := ca.IssueCertificate(*csr, ctx.reg.ID)
		test.AssertNotError(t, err, "Failed to sign certificate")
		if err != nil {
			continue
		}

		// Verify cert contents
		cert, err := x509.ParseCertificate(issuedCert.DER)
		test.AssertNotError(t, err, "Certificate failed to parse")

		test.AssertEquals(t, cert.Subject.CommonName, "not-example.com")

		switch len(cert.DNSNames) {
		case 1:
			if cert.DNSNames[0] != "not-example.com" {
				t.Errorf("Improper list of domain names %v", cert.DNSNames)
			}
		case 2:
			switch {
			case (cert.DNSNames[0] == "not-example.com" && cert.DNSNames[1] == "www.not-example.com"):
				t.Log("case 1")
			case (cert.DNSNames[0] == "www.not-example.com" && cert.DNSNames[1] == "not-example.com"):
				t.Log("case 2")
			default:
				t.Errorf("Improper list of domain names %v", cert.DNSNames)
			}

		default:
			t.Errorf("Improper list of domain names %v", cert.DNSNames)
		}

		// Test is broken by CFSSL Issue #156
		// https://github.com/cloudflare/cfssl/issues/156
		if len(cert.Subject.Country) > 0 {
			// Uncomment the Errorf as soon as upstream #156 is fixed
			// t.Errorf("Subject contained unauthorized values: %v", cert.Subject)
			t.Logf("Subject contained unauthorized values: %v", cert.Subject)
		}

		// Verify that the cert got stored in the DB
		serialString := core.SerialToString(cert.SerialNumber)
		if cert.Subject.SerialNumber != serialString {
			t.Errorf("SerialNumber: want %#v, got %#v", serialString, cert.Subject.SerialNumber)
		}
		storedCert, err := ctx.sa.GetCertificate(serialString)
		test.AssertNotError(t, err,
			fmt.Sprintf("Certificate %s not found in database", serialString))
		test.Assert(t, bytes.Equal(issuedCert.DER, storedCert.DER), "Retrieved cert not equal to issued cert.")

		certStatus, err := ctx.sa.GetCertificateStatus(serialString)
		test.AssertNotError(t, err,
			fmt.Sprintf("Error fetching status for certificate %s", serialString))
		test.Assert(t, certStatus.Status == core.OCSPStatusGood, "Certificate status was not good")
		test.Assert(t, certStatus.SubscriberApproved == false, "Subscriber shouldn't have approved cert yet.")
	}
}

func TestNoHostnames(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ca, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	test.AssertNotError(t, err, "Failed to create CA")
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	csr, _ := x509.ParseCertificateRequest(NoNamesCSR)
	_, err = ca.IssueCertificate(*csr, ctx.reg.ID)
	test.AssertError(t, err, "Issued certificate with no names")
	_, ok := err.(core.MalformedRequestError)
	test.Assert(t, ok, "Incorrect error type returned")
}

func TestRejectTooManyNames(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ca, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	test.AssertNotError(t, err, "Failed to create CA")
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	// Test that the CA rejects a CSR with too many names
	csr, _ := x509.ParseCertificateRequest(TooManyNameCSR)
	_, err = ca.IssueCertificate(*csr, ctx.reg.ID)
	test.AssertError(t, err, "Issued certificate with too many names")
	_, ok := err.(core.MalformedRequestError)
	test.Assert(t, ok, "Incorrect error type returned")
}

func TestDeduplication(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ca, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	test.AssertNotError(t, err, "Failed to create CA")
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	// Test that the CA collapses duplicate names
	csr, _ := x509.ParseCertificateRequest(DupeNameCSR)
	cert, err := ca.IssueCertificate(*csr, ctx.reg.ID)
	test.AssertNotError(t, err, "Failed to gracefully handle a CSR with duplicate names")

	parsedCert, err := x509.ParseCertificate(cert.DER)
	test.AssertNotError(t, err, "Error parsing certificate produced by CA")

	correctName := "a.not-example.com"
	correctNames := len(parsedCert.DNSNames) == 1 &&
		parsedCert.DNSNames[0] == correctName
	test.Assert(t, correctNames, "Incorrect set of names in deduplicated certificate")
}

func TestRejectValidityTooLong(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ca, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	test.AssertNotError(t, err, "Failed to create CA")
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	// This time is a few minutes before the notAfter in testdata/ca_cert.pem
	future, err := time.Parse(time.RFC822, "10 Feb 25 00:30 GMT")
	test.AssertNotError(t, err, "Failed to parse time")
	ctx.fc.Set(future)
	// Test that the CA rejects CSRs that would expire after the intermediate cert
	csr, _ := x509.ParseCertificateRequest(NoCNCSR)
	_, err = ca.IssueCertificate(*csr, 1)
	test.AssertError(t, err, "Cannot issue a certificate that expires after the intermediate certificate")
	_, ok := err.(core.InternalServerError)
	test.Assert(t, ok, "Incorrect error type returned")
}

func TestShortKey(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ca, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	// Test that the CA rejects CSRs that would expire after the intermediate cert
	csr, _ := x509.ParseCertificateRequest(ShortKeyCSR)
	_, err = ca.IssueCertificate(*csr, ctx.reg.ID)
	test.AssertError(t, err, "Issued a certificate with too short a key.")
	_, ok := err.(core.MalformedRequestError)
	test.Assert(t, ok, "Incorrect error type returned")
}

func TestAllowNoCN(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ca, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	test.AssertNotError(t, err, "Couldn't create new CA")
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	csr, err := x509.ParseCertificateRequest(NoCNCSR)
	test.AssertNotError(t, err, "Couldn't parse CSR")
	issuedCert, err := ca.IssueCertificate(*csr, ctx.reg.ID)
	test.AssertNotError(t, err, "Failed to sign certificate")
	cert, err := x509.ParseCertificate(issuedCert.DER)
	test.AssertNotError(t, err, fmt.Sprintf("unable to parse no CN cert: %s", err))
	if cert.Subject.CommonName != "" {
		t.Errorf("want no CommonName, got %#v", cert.Subject.CommonName)
	}
	serial := core.SerialToString(cert.SerialNumber)
	if cert.Subject.SerialNumber != serial {
		t.Errorf("SerialNumber: want %#v, got %#v", serial, cert.Subject.SerialNumber)
	}

	expected := []string{}
	for _, name := range csr.DNSNames {
		expected = append(expected, name)
	}
	sort.Strings(expected)
	actual := []string{}
	for _, name := range cert.DNSNames {
		actual = append(actual, name)
	}
	sort.Strings(actual)
	test.AssertDeepEquals(t, actual, expected)
}

func TestLongCommonName(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ca, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	csr, _ := x509.ParseCertificateRequest(LongCNCSR)
	_, err = ca.IssueCertificate(*csr, ctx.reg.ID)
	test.AssertError(t, err, "Issued a certificate with a CN over 64 bytes.")
	_, ok := err.(core.MalformedRequestError)
	test.Assert(t, ok, "Incorrect error type returned")
}

func TestRejectBadAlgorithm(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ca, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	// Test that the CA rejects CSRs that would expire after the intermediate cert
	csr, _ := x509.ParseCertificateRequest(BadAlgorithmCSR)
	_, err = ca.IssueCertificate(*csr, ctx.reg.ID)
	test.AssertError(t, err, "Issued a certificate based on a CSR with a weak algorithm.")
	_, ok := err.(core.MalformedRequestError)
	test.Assert(t, ok, "Incorrect error type returned")
}

func TestCapitalizedLetters(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ctx.caConfig.MaxNames = 3
	ca, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	csr, _ := x509.ParseCertificateRequest(CapitalizedCSR)
	cert, err := ca.IssueCertificate(*csr, ctx.reg.ID)
	test.AssertNotError(t, err, "Failed to gracefully handle a CSR with capitalized names")

	parsedCert, err := x509.ParseCertificate(cert.DER)
	test.AssertNotError(t, err, "Error parsing certificate produced by CA")
	test.AssertEquals(t, "capitalizedletters.com", parsedCert.Subject.CommonName)
	sort.Strings(parsedCert.DNSNames)
	expected := []string{"capitalizedletters.com", "evenmorecaps.com", "morecaps.com"}
	test.AssertDeepEquals(t, expected, parsedCert.DNSNames)
	t.Logf("subject serial number %#v", parsedCert.Subject.SerialNumber)
}

func TestWrongSignature(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ctx.caConfig.MaxNames = 3
	ca, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	// x509.ParseCertificateRequest() does not check for invalid signatures...
	csr, _ := x509.ParseCertificateRequest(WrongSignatureCSR)

	_, err = ca.IssueCertificate(*csr, ctx.reg.ID)
	if err == nil {
		t.Fatalf("Issued a certificate based on a CSR with an invalid signature.")
	}
}

func TestProfileSelection(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ctx.caConfig.MaxNames = 3
	ca, _ := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	testCases := []struct {
		CSR              []byte
		ExpectedKeyUsage x509.KeyUsage
	}{
		{CNandSANCSR, x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment},
		{ECDSACSR, x509.KeyUsageDigitalSignature},
	}

	for _, testCase := range testCases {
		csr, err := x509.ParseCertificateRequest(testCase.CSR)
		test.AssertNotError(t, err, "Cannot parse CSR")

		// Sign CSR
		issuedCert, err := ca.IssueCertificate(*csr, ctx.reg.ID)
		test.AssertNotError(t, err, "Failed to sign certificate")

		// Verify cert contents
		cert, err := x509.ParseCertificate(issuedCert.DER)
		test.AssertNotError(t, err, "Certificate failed to parse")

		t.Logf("expected key usage %v, got %v", testCase.ExpectedKeyUsage, cert.KeyUsage)
		test.AssertEquals(t, cert.KeyUsage, testCase.ExpectedKeyUsage)
	}
}

func countMustStaple(t *testing.T, cert *x509.Certificate) (count int) {
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(oidTLSFeature) {
			test.Assert(t, !ext.Critical, "Extension was marked critical")
			test.AssertByteEquals(t, ext.Value, mustStapleFeatureValue)
			count++
		}
	}
	return count
}

func TestExtensions(t *testing.T) {
	ctx := setup(t)
	defer ctx.cleanUp()
	ctx.caConfig.MaxNames = 3
	ca, err := NewCertificateAuthorityImpl(
		ctx.caConfig,
		ctx.fc,
		ctx.stats,
		ctx.issuers,
		ctx.keyPolicy)
	ca.Publisher = &mocks.Publisher{}
	ca.PA = ctx.pa
	ca.SA = ctx.sa

	mustStapleCSR, err := x509.ParseCertificateRequest(MustStapleCSR)
	test.AssertNotError(t, err, "Error parsing MustStapleCSR")

	duplicateMustStapleCSR, err := x509.ParseCertificateRequest(DuplicateMustStapleCSR)
	test.AssertNotError(t, err, "Error parsing DuplicateMustStapleCSR")

	tlsFeatureUnknownCSR, err := x509.ParseCertificateRequest(TLSFeatureUnknownCSR)
	test.AssertNotError(t, err, "Error parsing TLSFeatureUnknownCSR")

	unsupportedExtensionCSR, err := x509.ParseCertificateRequest(UnsupportedExtensionCSR)
	test.AssertNotError(t, err, "Error parsing UnsupportedExtensionCSR")

	sign := func(csr *x509.CertificateRequest) *x509.Certificate {
		coreCert, err := ca.IssueCertificate(*csr, ctx.reg.ID)
		test.AssertNotError(t, err, "Failed to issue")
		cert, err := x509.ParseCertificate(coreCert.DER)
		test.AssertNotError(t, err, "Error parsing certificate produced by CA")
		return cert
	}

	// With enableMustStaple = false, should issue successfully and not add
	// Must Staple.
	noStapleCert := sign(mustStapleCSR)
	test.AssertEquals(t, countMustStaple(t, noStapleCert), 0)

	// With enableMustStaple = true, a TLS feature extension should put a must-staple
	// extension into the cert
	ca.enableMustStaple = true
	singleStapleCert := sign(mustStapleCSR)
	test.AssertEquals(t, countMustStaple(t, singleStapleCert), 1)
	test.AssertEquals(t, ctx.stats.Counters[metricCSRExtensionTLSFeature], int64(2))

	// Even if there are multiple TLS Feature extensions, only one extension should be included
	duplicateMustStapleCert := sign(duplicateMustStapleCSR)
	test.AssertEquals(t, countMustStaple(t, duplicateMustStapleCert), 1)
	test.AssertEquals(t, ctx.stats.Counters[metricCSRExtensionTLSFeature], int64(3))

	// ... but if it doesn't ask for stapling, there should be an error
	_, err = ca.IssueCertificate(*tlsFeatureUnknownCSR, ctx.reg.ID)
	test.AssertError(t, err, "Allowed a CSR with an empty TLS feature extension")
	test.AssertEquals(t, ctx.stats.Counters[metricCSRExtensionTLSFeature], int64(4))
	test.AssertEquals(t, ctx.stats.Counters[metricCSRExtensionTLSFeatureInvalid], int64(1))

	// Unsupported extensions should be silently ignored, having the same
	// extensions as the TLS Feature cert above, minus the TLS Feature Extension
	unsupportedExtensionCert := sign(unsupportedExtensionCSR)
	test.AssertEquals(t, len(unsupportedExtensionCert.Extensions), len(singleStapleCert.Extensions)-1)
	test.AssertEquals(t, ctx.stats.Counters[metricCSRExtensionOther], int64(1))

	// None of the above CSRs have basic extensions
	test.AssertEquals(t, ctx.stats.Counters[metricCSRExtensionBasic], int64(0))
}
