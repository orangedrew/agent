/**
 * Copyright (c) F5, Inc.
 *
 * This source code is licensed under the Apache License, Version 2.0 license found in the
 * LICENSE file in the root directory of this source tree.
 */

package sdk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gogo/protobuf/types"
	"github.com/nginx/agent/sdk/v2/checksum"
	SDKfiles "github.com/nginx/agent/sdk/v2/files"
	"github.com/nginx/agent/sdk/v2/proto"
	"github.com/nginx/agent/sdk/v2/zip"
	crossplane "github.com/nginxinc/nginx-go-crossplane"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var directories = []string{
	"/tmp/testdata/configs",
	"/tmp/testdata/configs/other",
	"/tmp/testdata/logs",
	"/tmp/testdata/nginx",
	"/tmp/testdata/nginx/other",
	"/tmp/testdata/certs",
	"/tmp/testdata/root",
	"/tmp/testdata/foo",
	"/tmp/testdata/directoryMap/",
}

var files = []string{
	"/tmp/testdata/configs/empty_config.conf",
	"/tmp/testdata/configs/missing_fields.conf",
	"/tmp/testdata/configs/nginx-agent.conf",
	"/tmp/testdata/nginx/nginx.conf",
	"/tmp/testdata/nginx/nginx2.conf",
	"/tmp/testdata/nginx/other/hello.conf",
	"/tmp/testdata/nginx/other/goodbye.conf",
	"/tmp/testdata/nginx/other/mime.types",
	"/tmp/testdata/logs/access1.log",
	"/tmp/testdata/logs/access2.log",
	"/tmp/testdata/logs/error.log",
	"/tmp/testdata/root/test.html",
	"/tmp/testdata/foo/test.html",
	"/tmp/testdata/root/my-nap-policy.json",
	"/tmp/testdata/root/log-default.json",
}

var errorLogs = &proto.ErrorLogs{
	ErrorLog: []*proto.ErrorLog{
		{
			Name:        "/tmp/testdata/logs/error.log",
			LogLevel:    "info",
			Permissions: "0644",
			Readable:    true,
		},
	},
}

var accessLogs = &proto.AccessLogs{
	AccessLog: []*proto.AccessLog{
		{
			Name:        "/tmp/testdata/logs/access1.log",
			Format:      "$remote_addr - $remote_user [$time_local] \"$request\" $status $body_bytes_sent \"$http_referer\" \"$http_user_agent\" rt=$request_time uct=\"$upstream_connect_time\" uht=\"$upstream_header_time\" urt=\"$upstream_response_time\"",
			Permissions: "0644",
			Readable:    true,
		},
		{
			Name:        "/tmp/testdata/logs/access2.log",
			Format:      "combined",
			Permissions: "0644",
			Readable:    true,
		},
	},
}

const (
	nginxID  = "1"
	systemID = "2"
)

var tests = []struct {
	fileName         string
	config           string
	expected         *proto.NginxConfig
	plusApi          string
	expectedAuxFiles map[string]struct{}
}{
	{
		fileName: "/tmp/testdata/nginx/nginx.conf",
		config: `daemon            off;
		worker_processes  2;
		user              www-data;
		
		events {
			use           epoll;
			worker_connections  128;
		}
		
		error_log         /tmp/testdata/logs/error.log info;
				
		http {
			log_format upstream_time '$remote_addr - $remote_user [$time_local] '
			'"$request" $status $body_bytes_sent '
			'"$http_referer" "$http_user_agent" '
			'rt=$request_time uct="$upstream_connect_time" uht="$upstream_header_time" urt="$upstream_response_time"';
		
			server_tokens off;
			charset       utf-8;
		
			access_log    /tmp/testdata/logs/access1.log  $upstream_time;
			ssl_certificate     /tmp/testdata/nginx/ca.crt;
		
			server {
				server_name   localhost;
				listen        127.0.0.1:80;
		
				error_page    500 502 503 504  /50x.html;
				# ssl_certificate /usr/local/nginx/conf/cert.pem;
		
				location      / {
					root      /tmp/testdata/root;
					app_protect_enable on;
					app_protect_policy_file /tmp/testdata/root/my-nap-policy.json;
					app_protect_security_log_enable on;
					app_protect_security_log "/tmp/testdata/root/log-default.json" /var/log/app_protect/security.log;		
				}

				location /privateapi {
					limit_except GET {
						auth_basic "NGINX Plus API";
						auth_basic_user_file /path/to/passwd/file;
					}
					api write=on;
					allow 127.0.0.1;
					deny  all;
				}	
			}
		
			access_log    /tmp/testdata/logs/access2.log  combined;
		
		}`,
		plusApi: "http://127.0.0.1:80/privateapi",
		expected: &proto.NginxConfig{
			Action: proto.NginxConfigAction_RETURN,
			DirectoryMap: &proto.DirectoryMap{
				Directories: []*proto.Directory{
					{
						Name:        "/tmp/testdata/nginx",
						Permissions: "0755",
						Files: []*proto.File{
							{
								Name:        "nginx.conf",
								Permissions: "0644",
								Size_:       959,
							},
							{
								Name:        "ca.crt",
								Permissions: "0644",
								Size_:       959,
							},
						},
						Size_: 128,
					},
					{
						Name:        "/tmp/testdata/root",
						Permissions: "0755",
						Files: []*proto.File{
							{
								Name:        "log-default.json",
								Permissions: "0644",
								Size_:       959,
							},
							{
								Name:        "my-nap-policy.json",
								Permissions: "0644",
								Size_:       959,
							},
							{
								Name:        "test.html",
								Permissions: "0644",
								Size_:       959,
							},
						},
						Size_: 128,
					},
				},
			},
			AccessLogs: accessLogs,
			ErrorLogs:  errorLogs,
			ConfigData: &proto.ConfigDescriptor{
				NginxId:  nginxID,
				SystemId: systemID,
				Checksum: "",
			},
			Ssl: &proto.SslCertificates{
				SslCerts: []*proto.SslCertificate{
					{
						FileName: "/tmp/testdata/nginx/ca.crt",
						Validity: &proto.CertificateDates{
							NotBefore: 1632834204,
							NotAfter:  1635426204,
						},
						Issuer: &proto.CertificateName{
							CommonName:         "ca.local",
							Country:            []string{"IE"},
							Locality:           []string{"Cork"},
							Organization:       []string{"NGINX"},
							OrganizationalUnit: nil,
						},
						Subject: &proto.CertificateName{
							CommonName:         "ca.local",
							Country:            []string{"IE"},
							Locality:           []string{"Cork"},
							Organization:       []string{"NGINX"},
							State:              []string{"Cork"},
							OrganizationalUnit: nil,
						},
						Size_:                  1926,
						Mtime:                  &types.Timestamp{Seconds: 1633343804, Nanos: 15240107},
						SubjAltNames:           nil,
						PublicKeyAlgorithm:     "RSA",
						SignatureAlgorithm:     "SHA512-RSA",
						SerialNumber:           "12554968962670027276",
						SubjectKeyIdentifier:   "75:50:E2:24:8F:6F:13:1D:81:20:E1:01:0B:57:2B:98:39:E5:2E:C3",
						Fingerprint:            "48:6D:05:D4:78:10:91:15:69:74:9C:6A:54:F7:F2:FC:C8:93:46:E8:28:42:24:41:68:41:51:1E:1E:43:E0:12",
						FingerprintAlgorithm:   "SHA512-RSA",
						AuthorityKeyIdentifier: "3A:79:E0:3E:61:CD:94:29:1D:BB:45:37:0B:E9:78:E9:2F:40:67:CA",
						Version:                3,
					},
				},
			},
			Zaux: &proto.ZippedFile{
				Checksum:      "ff5c9e0b439bc85f6c62dc4d794d94250c7b98093b3b3202e6b5a63a235a5216",
				RootDirectory: "/tmp/testdata/root",
			},
			Zconfig: &proto.ZippedFile{
				Contents:      []uint8{31, 139, 8, 0, 0, 0, 0, 0, 0, 255, 1, 0, 0, 255, 255, 0, 0, 0, 0, 0, 0, 0, 0},
				Checksum:      "4b9cb7001222fcbf2a24e3409b264302a8a8f22f28c4a1830e065f0986dd57e6",
				RootDirectory: "/tmp/testdata/nginx",
			},
		},
		expectedAuxFiles: map[string]struct{}{
			"/tmp/testdata/root/test.html":          {},
			"/tmp/testdata/nginx/ca.crt":            {},
			"/tmp/testdata/root/my-nap-policy.json": {},
			"/tmp/testdata/root/log-default.json":   {},
		},
	},
	{
		fileName: "/tmp/testdata/nginx/nginx2.conf",
		config: `daemon            off;
		worker_processes  2;
		user              www-data;
		
		events {
			use           epoll;
			worker_connections  128;
		}
		
		error_log         /tmp/testdata/logs/error.log info;
				
		http {
			log_format upstream_time '$remote_addr - $remote_user [$time_local] '
			'"$request" $status $body_bytes_sent '
			'"$http_referer" "$http_user_agent" '
			'rt=$request_time uct="$upstream_connect_time" uht="$upstream_header_time" urt="$upstream_response_time"';
		
			server_tokens off;
			charset       utf-8;
		
			access_log    /tmp/testdata/logs/access1.log  $upstream_time;
			ssl_certificate     /tmp/testdata/nginx/ca.crt;
		
			server {
				server_name   localhost;
				listen        127.0.0.1:80;
		
				error_page    500 502 503 504  /50x.html;
				# ssl_certificate /usr/local/nginx/conf/cert.pem;
		
				location      / {
					root      /tmp/testdata/foo;
				}

				location /stub_status {
					stub_status;
				}
			}
		
			access_log    /tmp/testdata/logs/access2.log  combined;
		
		}`,
		plusApi: "http://127.0.0.1:80/stub_status",
		expected: &proto.NginxConfig{
			Action: proto.NginxConfigAction_RETURN,
			DirectoryMap: &proto.DirectoryMap{
				Directories: []*proto.Directory{
					{
						Name:        "/tmp/testdata/foo",
						Permissions: "0755",
						Files: []*proto.File{
							{
								Name:        "test.html",
								Permissions: "0644",
								Size_:       959,
							},
						},
						Size_: 128,
					},
					{
						Name:        "/tmp/testdata/nginx",
						Permissions: "0755",
						Files: []*proto.File{
							{
								Name:        "nginx2.conf",
								Permissions: "0644",
								Size_:       959,
							},
							{
								Name:        "ca.crt",
								Permissions: "0644",
								Size_:       959,
							},
						},
						Size_: 128,
					},
				},
			},
			AccessLogs: accessLogs,
			ErrorLogs:  errorLogs,
			ConfigData: &proto.ConfigDescriptor{
				NginxId:  nginxID,
				SystemId: systemID,
				Checksum: "",
			},
			Ssl: &proto.SslCertificates{
				SslCerts: []*proto.SslCertificate{
					{
						FileName: "/tmp/testdata/nginx/ca.crt",
						Validity: &proto.CertificateDates{
							NotBefore: 1632834204,
							NotAfter:  1635426204,
						},
						Issuer: &proto.CertificateName{
							CommonName:         "ca.local",
							Country:            []string{"IE"},
							Locality:           []string{"Cork"},
							Organization:       []string{"NGINX"},
							OrganizationalUnit: nil,
						},
						Subject: &proto.CertificateName{
							CommonName:         "ca.local",
							Country:            []string{"IE"},
							Locality:           []string{"Cork"},
							Organization:       []string{"NGINX"},
							State:              []string{"Cork"},
							OrganizationalUnit: nil,
						},
						Size_:                  1926,
						Mtime:                  &types.Timestamp{Seconds: 1633343804, Nanos: 15240107},
						SubjAltNames:           nil,
						PublicKeyAlgorithm:     "RSA",
						SignatureAlgorithm:     "SHA512-RSA",
						SerialNumber:           "12554968962670027276",
						SubjectKeyIdentifier:   "75:50:E2:24:8F:6F:13:1D:81:20:E1:01:0B:57:2B:98:39:E5:2E:C3",
						Fingerprint:            "48:6D:05:D4:78:10:91:15:69:74:9C:6A:54:F7:F2:FC:C8:93:46:E8:28:42:24:41:68:41:51:1E:1E:43:E0:12",
						AuthorityKeyIdentifier: "3A:79:E0:3E:61:CD:94:29:1D:BB:45:37:0B:E9:78:E9:2F:40:67:CA",
						FingerprintAlgorithm:   "SHA512-RSA",
						Version:                3,
					},
				},
			},
			// using RootDirectory for allowed in the tests, but the "root" directive is /tmp/testdata/foo, so
			// should have an empty file list from the aux
			Zaux: &proto.ZippedFile{
				Checksum:      "51c05b653bc43deb5ec497988692fc5dec05ab8b6a0b78e908e4628b3d9e0d4c",
				RootDirectory: "/tmp/testdata/foo",
			},
			Zconfig: &proto.ZippedFile{
				Contents:      []uint8{31, 139, 8, 0, 0, 0, 0, 0, 0, 255, 1, 0, 0, 255, 255, 0, 0, 0, 0, 0, 0, 0, 0},
				Checksum:      "29fb1bed60766983ba835c80b3f4faf8aae145094e4d0b8b9cf5cb6b2bc3a9c3",
				RootDirectory: "/tmp/testdata/nginx",
			},
		},
		expectedAuxFiles: map[string]struct{}{
			"/tmp/testdata/foo/test.html": {},
			"/tmp/testdata/nginx/ca.crt":  {},
		},
	},
	{
		fileName: "/tmp/testdata/nginx/hello.conf",
		config: `var config = user CONTROLLER_USER_SUB;
		worker_processes auto;
		pid /run/nginx.pid;
		
		# distro version of nginx have stream has dynamic module
		load_module modules/ngx_stream_module.so;
		
		events {
			worker_connections 1024;
		}
		
		stream {
			log_format stream '$remote_addr [$time_local] stream L:$server_port R:$upstream_addr '
			'Status: $status bytes sent: $bytes_sent bytes received: $bytes_received '
			'session duration: $session_time '
			'TLS protocol: $ssl_protocol cipher: $ssl_cipher ssl_client_verify: $ssl_client_verify '
			'ssl_preread_server_name: $ssl_preread_server_name server_name: $ssl_server_name';
		
			include other/hello.conf;
		}
		
		http {
			include /tmp/testdata/nginx/other/mime.types;
			ssl_protocols       TLSv1.1 TLSv1.2;
			ssl_ciphers         HIGH:!aNULL:!MD5;
			ssl_session_cache   shared:SSL:10m;
			ssl_session_timeout 10m;
			log_format main  '$remote_addr - $remote_user [$time_local] "$request" '
				'$status $body_bytes_sent "$http_referer" '
				'"$http_user_agent" "$http_x_forwarded_for" '
				'rt=$request_time uct="$upstream_connect_time" uht="$upstream_header_time" urt="$upstream_response_time"';
			server_tokens off;
		
			client_max_body_size 5M;
			limit_req_zone $binary_remote_addr zone=ratelimit:10m rate=20r/s;
			limit_req_zone $binary_remote_addr zone=strict-ratelimit:10m rate=1r/s;
		
		
			# include /tmp/testdata/nginx/other/mimez.types;
		
			access_log /tmp/testdata/logs/access2.log;
			error_log /tmp/testdata/logs/error.log;
		
			map $http_upgrade $connection_upgrade {
				default upgrade;
				'' close;
			}

			server {
				listen 192.168.1.23;

				location /api {
					limit_except GET {
						auth_basic "NGINX Plus API";
						auth_basic_user_file /path/to/passwd/file;
					}
					api write=on;
					allow 127.0.0.1;
					deny  all;
				}
			}
			include /tmp/testdata/nginx/other/goodbye.conf;
		}`,
		plusApi: "",
		expected: &proto.NginxConfig{
			Action: proto.NginxConfigAction_RETURN,
			DirectoryMap: &proto.DirectoryMap{
				Directories: []*proto.Directory{
					{
						Name:        "/tmp/testdata/nginx",
						Permissions: "0755",
						Files: []*proto.File{
							{
								Name:        "hello.conf",
								Permissions: "0644",
								Size_:       1672,
							},
						},
						Size_: 160,
					},
					{
						Name:        "/tmp/testdata/nginx/other",
						Permissions: "0755",
						Files: []*proto.File{
							{
								Name:        "hello.conf",
								Permissions: "0644",
							},
							{
								Name:        "mime.types",
								Permissions: "0644",
							},
							{
								Name:        "goodbye.conf",
								Permissions: "0644",
							},
						},
						Size_: 160,
					},
				},
			},
			AccessLogs: &proto.AccessLogs{
				AccessLog: []*proto.AccessLog{
					{
						Name:        "/tmp/testdata/logs/access2.log",
						Format:      "",
						Permissions: "0644",
						Readable:    true,
					},
				},
			},
			ErrorLogs: &proto.ErrorLogs{
				ErrorLog: []*proto.ErrorLog{
					{
						Name:        "/tmp/testdata/logs/error.log",
						Permissions: "0644",
						Readable:    true,
					},
				},
			},
			ConfigData: &proto.ConfigDescriptor{
				NginxId:  "1",
				SystemId: "2",
				Checksum: "",
			},
			Ssl: &proto.SslCertificates{
				SslCerts: []*proto.SslCertificate{},
			},
			Zaux: nil,
			Zconfig: &proto.ZippedFile{
				Checksum:      "1e4bebfb74c215d6bd247ef1c4452cfa8973804abe190725a317d0230b4e6a67",
				RootDirectory: "/tmp/testdata/nginx",
			},
		},
	},
}

func TestGetNginxConfigFiles(t *testing.T) {
	for _, test := range tests {
		config := &proto.NginxConfig{}
		err := setUpDirectories()
		assert.NoError(t, err)
		defer tearDownDirectories()

		err = setUpFile(test.fileName, []byte(test.config))
		assert.Nil(t, err)

		conf, err := zip.NewWriter(test.fileName)
		assert.NoError(t, err)

		err = conf.AddFile(test.fileName)
		assert.NoError(t, err)

		config.Zconfig, err = conf.Proto()
		assert.NoError(t, err)
		assert.NotNil(t, config.GetZconfig())

		if test.expected.Zaux != nil {
			aux, err := zip.NewWriter(test.fileName)
			assert.NoError(t, err)

			err = aux.AddFile(test.fileName)
			assert.NoError(t, err)

			config.Zaux, err = aux.Proto()
			assert.NoError(t, err)
			assert.NotNil(t, config.GetZaux())
		}

		configFiles, auxFiles, err := GetNginxConfigFiles(config)
		assert.NoError(t, err)
		assert.NotNil(t, configFiles)
		assert.NotEmpty(t, configFiles)

		for _, file := range configFiles {
			assert.Equal(t, test.config, string(file.GetContents()))
		}

		if test.expected.Zaux != nil {
			assert.NotNil(t, auxFiles)
			assert.NotEmpty(t, auxFiles)
		}
	}
}

func TestGetNginxConfig(t *testing.T) {
	for _, test := range tests {
		err := setUpDirectories()
		assert.NoError(t, err)
		defer tearDownDirectories()

		err = setUpFile(test.fileName, []byte(test.config))
		assert.NoError(t, err)

		err = generateCertificate()
		assert.NoError(t, err)

		allowedDirs := map[string]struct{}{}

		if test.expected.Zaux != nil {
			allowedDirs[test.expected.Zaux.RootDirectory] = struct{}{}
			allowedDirs["/tmp/testdata/nginx/"] = struct{}{}
		}
		result, err := GetNginxConfig(test.fileName, nginxID, systemID, allowedDirs)
		assert.NoError(t, err)

		assert.Equal(t, test.expected.Action, result.Action)
		assert.Equal(t, len(test.expected.DirectoryMap.Directories), len(result.DirectoryMap.Directories))
		for dirIndex, expectedDirectory := range test.expected.DirectoryMap.Directories {
			resultDir := result.DirectoryMap.Directories[dirIndex]
			assert.Equal(t, expectedDirectory.Name, resultDir.Name)
			assert.Equal(t, expectedDirectory.Permissions, resultDir.Permissions)

			assert.Equal(t, len(expectedDirectory.Files), len(resultDir.Files))
			for fileIndex, expectedFile := range expectedDirectory.Files {
				resultFile := resultDir.Files[fileIndex]
				assert.Equal(t, expectedFile.Name, resultFile.Name)
				assert.Equal(t, expectedFile.Permissions, resultFile.Permissions)
			}
		}

		for i := range test.expected.Ssl.SslCerts {
			filename := test.expected.Ssl.SslCerts[i].FileName

			size, timestamp := getModTime(filename)
			test.expected.Ssl.SslCerts[i].Mtime = timestamp
			test.expected.Ssl.SslCerts[i].Size_ = size

			crtMeta := getCertMeta(filename)
			test.expected.Ssl.SslCerts[i].Validity.NotBefore = crtMeta.notBefore
			test.expected.Ssl.SslCerts[i].Validity.NotAfter = crtMeta.notAfter
			test.expected.Ssl.SslCerts[i].SerialNumber = crtMeta.serialNumber
			test.expected.Ssl.SslCerts[i].Fingerprint = crtMeta.fingerprint
			test.expected.Ssl.SslCerts[i].SubjectKeyIdentifier = crtMeta.subjectKeyIdentifier
			test.expected.Ssl.SslCerts[i].AuthorityKeyIdentifier = crtMeta.authKeyIdentifier
		}

		assert.Equal(t, test.expected.AccessLogs, result.AccessLogs)
		assert.Equal(t, test.expected.ErrorLogs, result.ErrorLogs)
		assert.Equal(t, test.expected.ConfigData, result.ConfigData)
		assert.Equal(t, test.expected.Ssl, result.Ssl)
		assert.Equal(t, test.expected.Zconfig.Checksum, result.Zconfig.Checksum)

		r, err := zip.NewReader(result.Zconfig)
		require.NoError(t, err)
		expectedFileContent := map[string][]byte{test.fileName: []byte(test.config)}
		r.RangeFileReaders(func(err error, path string, mode os.FileMode, r io.Reader) bool {
			var b []byte
			b, err = io.ReadAll(r)
			require.NoError(t, err)
			if bb, ok := expectedFileContent[path]; ok {
				require.Equal(t, bb, b, path)
				delete(expectedFileContent, path)
			} else {
				bb, err = os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, bb, b, path)
			}
			return true
		})
		assert.Empty(t, expectedFileContent)

		if test.expected.Zaux != nil {
			assert.NotNil(t, result.Zaux)

			// need to update the checksum because new certificates are generated each test
			test.expected.Zaux.Checksum = checksum.HexChecksum(result.Zaux.Contents)

			assert.Equal(t, test.expected.Zaux.Checksum, result.Zaux.Checksum)
			zf, err := zip.NewReader(result.Zaux)
			assert.NoError(t, err)
			files := make(map[string]struct{})
			zf.RangeFileReaders(func(err error, path string, mode os.FileMode, r io.Reader) bool {
				files[path] = struct{}{}
				return true
			})
			assert.Equal(t, test.expectedAuxFiles, files)
		}
	}
}

func TestGetStatusApiInfo(t *testing.T) {
	log.SetOutput(io.Discard)

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.URL.String() == "/privateapi" {
			data := []byte("api OK")
			_, err := rw.Write(data)
			assert.Nil(t, err)
		} else if req.URL.String() == "/stub_status" {
			rw.WriteHeader(http.StatusInternalServerError)
			data := []byte("stub_status OK")
			_, err := rw.Write(data)
			assert.Nil(t, err)
		}
	}))
	defer server.Close()

	for _, test := range tests {
		t.Run(test.fileName, func(t *testing.T) {
			err := setUpDirectories()
			require.NoError(t, err)

			err = setUpFiles()
			require.Nil(t, err)

			// Replace ip & ports in config with mock server ip & port
			input, err := ioutil.ReadFile(test.fileName)
			assert.Nil(t, err)
			splitUrl := strings.Split(server.URL, "//")[1]

			output := bytes.Replace(input, []byte("127.0.0.1:80"), []byte(splitUrl), -1)
			assert.NoError(t, ioutil.WriteFile(test.fileName, output, 0666))

			result, err := GetStatusApiInfo(test.fileName)

			//Update port in expected plusApi with the port of the mock server
			test.plusApi = strings.Replace(test.plusApi, ":80", ":"+strings.Split(splitUrl, ":")[1], 1)

			assert.Equal(t, test.plusApi, result)
			if test.plusApi != "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}

			tearDownDirectories()
		})
	}
}

func TestParseStatusAPIEndpoints(t *testing.T) {
	tmpDir := t.TempDir()
	for _, tt := range []struct {
		oss  []string
		plus []string
		conf string
	}{
		{
			plus: []string{
				"http://localhost:80/api/",
			},
			conf: `
server {
    listen       80 default_server;
    server_name  localhost;
    location /api/ {
        api write=on;
        allow 127.0.0.1;
        deny all;
    }
}
`,
		},
		{
			plus: []string{
				"http://localhost:80/api/",
			},
			conf: `
server {
    listen       *:80 default_server;
    location /api/ {
        api write=on;
        allow 127.0.0.1;
        deny all;
    }
}
`,
		},
		{
			plus: []string{
				"http://localhost:80/api/",
			},
			conf: `
server {
    listen       80 default_server;
	server_name _;
    location /api/ {
        api write=on;
        allow 127.0.0.1;
        deny all;
    }
}
`,
		},
		{
			plus: []string{
				"http://127.0.0.1:8080/privateapi",
			},
			conf: `
		server {
		       listen 127.0.0.1:8080;
		       location /privateapi {
		           api write=on;
		           allow 127.0.0.1;
		           deny all;
		       }
		}
		`,
		},
		{
			plus: []string{
				"http://localhost:80/api/",
			},
			conf: `
server {
    listen 80 default_server;
	listen [::]:80 default_server;
	server_name _;
    location /api/ {
        api write=on;
        allow 127.0.0.1;
        deny all;
    }
}
`,
		},
		{
			plus: []string{
				"http://127.0.0.1:80/api/",
			},
			conf: `
server {
    listen 127.0.0.1;
	server_name _;
    location /api/ {
        api write=on;
        allow 127.0.0.1;
        deny all;
    }
}
`,
		},
		{
			plus: []string{
				"http://localhost:80/api/",
			},
			conf: `
server {
    listen localhost;
	server_name _;
    location /api/ {
        api write=on;
        allow 127.0.0.1;
        deny all;
    }
}
`,
		},
		{
			plus: []string{
				"http://[::1]:80/api/",
			},
			conf: `
server {
    listen [::1];
	server_name _;
    location /api/ {
        api write=on;
        allow 127.0.0.1;
        deny all;
    }
}
`,
		},
		{
			plus: []string{
				"http://localhost:8000/api/",
			},
			conf: `
server {
    listen [::]:8000;
	server_name _;
    location /api/ {
        api write=on;
        allow 127.0.0.1;
        deny all;
    }
}
`,
		},
		{
			oss: []string{
				"http://127.0.0.1:80/stub_status",
			},
			conf: `
		server {
			server_name   localhost;
			listen        127.0.0.1:80;
		
			error_page    500 502 503 504  /50x.html;
			# ssl_certificate /usr/local/nginx/conf/cert.pem;
		
			location      / {
				root      /tmp/testdata/foo;
			}
		
			location /stub_status {
				stub_status;
			}
		}
		`,
		},
	} {
		f, err := ioutil.TempFile(tmpDir, "conf")
		assert.NoError(t, err)

		_, err = f.Write([]byte(fmt.Sprintf("http{ %s }", tt.conf)))
		assert.NoError(t, err)
		payload, err := crossplane.Parse(f.Name(),
			&crossplane.ParseOptions{
				SingleFile:         false,
				StopParsingOnError: true,
			},
		)
		assert.NoError(t, err)
		var oss []string
		var plus []string
		assert.Equal(t, len(payload.Config), 1)
		for _, xpConf := range payload.Config {
			assert.Equal(t, len(xpConf.Parsed), 1)
			err = CrossplaneConfigTraverse(&xpConf, func(parent *crossplane.Directive, current *crossplane.Directive) (bool, error) {
				_plus, _oss := parseStatusAPIEndpoints(parent, current)
				oss = append(oss, _oss...)
				plus = append(plus, _plus...)
				return true, nil
			})
			assert.NoError(t, err)

		}

		assert.Equal(t, tt.plus, plus)
		assert.Equal(t, tt.oss, oss)
	}
}

func TestGetErrorAndAccessLogs(t *testing.T) {
	for _, test := range tests {
		err := setUpDirectories()
		assert.NoError(t, err)

		err = setUpFile(test.fileName, []byte(test.config))
		assert.NoError(t, err)

		errorLogs, accessLogs, err := GetErrorAndAccessLogs(test.fileName)
		assert.NoError(t, err)

		for index, accessLog := range accessLogs.AccessLog {
			assert.Equal(t, test.expected.AccessLogs.AccessLog[index].Name, accessLog.Name)
		}
		for index, errorLog := range errorLogs.ErrorLog {
			assert.Equal(t, test.expected.ErrorLogs.ErrorLog[index].Name, errorLog.Name)
		}
		tearDownDirectories()
	}
}

func TestGetAccessLogs(t *testing.T) {
	result := GetAccessLogs(accessLogs)
	assert.Equal(t, []string{"/tmp/testdata/logs/access1.log", "/tmp/testdata/logs/access2.log"}, result)
}

func TestGetErrorLogs(t *testing.T) {
	result := GetErrorLogs(errorLogs)
	assert.Equal(t, []string{"/tmp/testdata/logs/error.log"}, result)
}

func setUpDirectories() error {
	tearDownDirectories()
	for _, dir := range directories {
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return err
		}
	}

	for _, file := range files {
		err := ioutil.WriteFile(file, []byte{}, 0644)
		if err != nil {
			return err
		}
	}

	return nil
}

func setUpFiles() error {
	for _, file := range files {
		err := setUpFile(file, []byte{})
		if err != nil {
			return err
		}
	}

	for _, test := range tests {
		err := setUpFile(test.fileName, []byte(test.config))
		if err != nil {
			return err
		}
	}
	return nil
}

func setUpFile(file string, content []byte) error {
	err := os.MkdirAll(filepath.Dir(file), 0755)
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(file, content, 0644)
	if err != nil {
		return err
	}

	return nil
}

func tearDownDirectories() {
	for _, dir := range directories {
		os.RemoveAll(dir)
	}
	os.RemoveAll("/tmp/testdata")
}

func getModTime(file string) (int64, *types.Timestamp) {
	info, err := os.Stat(file)
	if err == nil {
		return int64(info.Size()), SDKfiles.TimeConvert(info.ModTime())
	}
	return 0, nil
}

type crtMetaFields struct {
	notBefore            int64
	notAfter             int64
	serialNumber         string
	fingerprint          string
	subjectKeyIdentifier string
	authKeyIdentifier    string
}

func getCertMeta(file string) crtMetaFields {
	r := crtMetaFields{}
	cert, err := LoadCertificate(file)
	if err != nil {
		return r
	}

	fingerprint := sha256.Sum256(cert.Raw)
	return crtMetaFields{
		notBefore:            cert.NotBefore.Unix(),
		notAfter:             cert.NotAfter.Unix(),
		serialNumber:         cert.SerialNumber.String(),
		subjectKeyIdentifier: convertToHexFormat(hex.EncodeToString(cert.SubjectKeyId)),
		fingerprint:          convertToHexFormat(hex.EncodeToString(fingerprint[:])),
		authKeyIdentifier:    convertToHexFormat(hex.EncodeToString(cert.AuthorityKeyId)),
	}
}

func generateCertificate() error {
	cmd := exec.Command("../scripts/tls/gen_cnf.sh", "ca", "--cn", "'ca.local'", "--state", "Cork", "--locality", "Cork", "--org", "NGINX", "--country", "IE", "--out", "certs/conf")

	err := cmd.Run()
	if err != nil {
		return err
	}

	cmd1 := exec.Command("../scripts/tls/gen_cert.sh", "ca", "--config", "certs/conf/ca.cnf", "--out", "/tmp/testdata/nginx/")

	err = cmd1.Run()
	if err != nil {
		return err
	}

	return nil
}

func TestUpdateNginxConfigFileWithRoot(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedReads int
	}{
		{
			name:          "one root",
			input:         `root foo/bar;`,
			expectedReads: 1,
		},
		{
			name: "duplicate root",
			input: `root foo/bar;
root foo/bar;`,
			expectedReads: 1,
		},
		{
			name: "overlapping root(recursive)",
			input: `root foo/bar/baz;
root foo/bar;`,
			expectedReads: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			f, err := ioutil.TempFile(tmpDir, "conf")
			require.NoError(t, err)

			_, err = f.WriteString(tt.input)
			assert.NoError(t, err)

			auxWriter, err := zip.NewWriter(filepath.Dir(f.Name()))
			require.NoError(t, err)

			seen := make(map[string]struct{})
			allowedDirectories := make(map[string]struct{})
			allowedDirectories[f.Name()] = struct{}{}
			directoryPathMap := newDirectoryMap()

			reader, err := os.Open(f.Name())
			require.NoError(t, err)
			defer reader.Close()

			err = auxWriter.Add(f.Name(), fs.FileMode(os.O_RDWR), reader)
			assert.NoError(t, err)

			err = updateNginxConfigFileWithRoot(auxWriter, f.Name(), seen, allowedDirectories, directoryPathMap)
			assert.NoError(t, err)

			aux, err := auxWriter.Proto()
			assert.NoError(t, err)
			assert.NotNil(t, aux)

			// one file read is expected in the auxWriter per unique root dir
			assert.Equal(t, tt.expectedReads, auxWriter.FileLen())
		})
	}
}

func TestUpdateNginxConfigFileWithAuxFile(t *testing.T) {
	var myTests = []struct {
		fileName         string
		content          string
		allowDir         string
		expected         *proto.NginxConfig
		expectedAuxFiles map[string]struct{}
	}{
		{
			fileName: "/tmp/testdata/app_protect_metadata.json",
			content:  "{\"hello\": \"world\"}",
			allowDir: "/tmp/testdata",
			expected: &proto.NginxConfig{
				DirectoryMap: &proto.DirectoryMap{
					Directories: []*proto.Directory{
						{
							Name:        "/tmp/testdata",
							Permissions: "0755",
							Files: []*proto.File{
								{
									Name:        "app_protect_metadata.json",
									Permissions: "0644",
									Size_:       959,
								},
							},
							Size_: 128,
						},
					},
				},
				Zaux: &proto.ZippedFile{
					Checksum:      "c660937641a883c1291a9cde1a0e0e61a926fe17c2f2b18af2ee05382d7d5b49",
					RootDirectory: "/tmp/testdata",
				},
			},
			expectedAuxFiles: map[string]struct{}{
				"/tmp/testdata/app_protect_metadata.json": {},
			},
		},
	}

	for _, test := range myTests {
		err := setUpDirectories()
		assert.NoError(t, err)
		defer tearDownDirectories()

		err = setUpFile(test.fileName, []byte(test.content))
		assert.NoError(t, err)

		allowedDirs := map[string]struct{}{}
		allowedDirs[test.allowDir] = struct{}{}

		aux, err := zip.NewWriter(filepath.Dir(test.allowDir))
		assert.NoError(t, err)

		nginxConfig := &proto.NginxConfig{
			Action: proto.NginxConfigAction_RETURN,
			ConfigData: &proto.ConfigDescriptor{
				NginxId:  nginxID,
				SystemId: systemID,
			},
			Zconfig:      nil,
			Zaux:         nil,
			AccessLogs:   &proto.AccessLogs{AccessLog: make([]*proto.AccessLog, 0)},
			ErrorLogs:    &proto.ErrorLogs{ErrorLog: make([]*proto.ErrorLog, 0)},
			Ssl:          &proto.SslCertificates{SslCerts: make([]*proto.SslCertificate, 0)},
			DirectoryMap: &proto.DirectoryMap{Directories: make([]*proto.Directory, 0)},
		}

		seen := make(map[string]struct{}) // local cache of seen files

		directoryMap := &DirectoryMap{make(map[string]*proto.Directory)}

		err = updateNginxConfigFileWithAuxFile(aux, test.fileName, nginxConfig, seen, allowedDirs, directoryMap, true)
		assert.NoError(t, err)

		if test.expected.Zaux != nil {
			assert.NotNil(t, aux)
			auxProto, err := aux.Proto()
			assert.NoError(t, err)

			assert.Equal(t, test.expected.Zaux.Checksum, auxProto.Checksum)
			zf, err := zip.NewReader(auxProto)
			assert.NoError(t, err)
			expectedFiles := make(map[string]struct{})
			zf.RangeFileReaders(func(err error, path string, mode os.FileMode, r io.Reader) bool {
				expectedFiles[path] = struct{}{}
				var b []byte
				b, err = io.ReadAll(r)
				require.NoError(t, err)
				var bb []byte
				bb, err = os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, bb, b)
				return true
			})
			assert.Equal(t, test.expectedAuxFiles, expectedFiles)
		}

		setDirectoryMap(directoryMap, nginxConfig)
		assert.Equal(t, len(test.expected.DirectoryMap.Directories), len(nginxConfig.DirectoryMap.Directories))
		for dirIndex, expectedDirectory := range test.expected.DirectoryMap.Directories {
			resultDir := nginxConfig.DirectoryMap.Directories[dirIndex]
			assert.Equal(t, expectedDirectory.Name, resultDir.Name)
			assert.Equal(t, expectedDirectory.Permissions, resultDir.Permissions)

			assert.Equal(t, len(expectedDirectory.Files), len(resultDir.Files))
			for fileIndex, expectedFile := range expectedDirectory.Files {
				resultFile := resultDir.Files[fileIndex]
				assert.Equal(t, expectedFile.Name, resultFile.Name)
				assert.Equal(t, expectedFile.Permissions, resultFile.Permissions)
			}
		}
	}
}

func TestAddAuxfileToNginxConfig(t *testing.T) {
	var tests = []struct {
		fileName         string
		content          string
		allowDir         string
		expected         *proto.NginxConfig
		expectedAuxFiles map[string]struct{}
	}{
		{
			fileName: "/tmp/testdata/app_protect_metadata.json",
			content:  "{\"hello\": \"world\"}",
			allowDir: "/tmp/testdata",
			expected: &proto.NginxConfig{
				DirectoryMap: &proto.DirectoryMap{
					Directories: []*proto.Directory{
						{
							Name:        "/tmp/testdata",
							Permissions: "0755",
							Files: []*proto.File{
								{
									Name:        "app_protect_metadata.json",
									Permissions: "0644",
									Size_:       959,
								},
							},
							Size_: 128,
						},
					},
				},
				Zaux: &proto.ZippedFile{
					Checksum:      "c660937641a883c1291a9cde1a0e0e61a926fe17c2f2b18af2ee05382d7d5b49",
					RootDirectory: "/tmp/testdata",
				},
			},
			expectedAuxFiles: map[string]struct{}{
				"/tmp/testdata/app_protect_metadata.json": {},
			},
		},
	}

	for _, test := range tests {
		err := setUpDirectories()
		assert.NoError(t, err)
		defer tearDownDirectories()

		err = setUpFile(test.fileName, []byte(test.content))
		assert.NoError(t, err)

		allowedDirs := map[string]struct{}{}
		allowedDirs[test.allowDir] = struct{}{}

		nginxConfig := &proto.NginxConfig{
			Action: proto.NginxConfigAction_RETURN,
			ConfigData: &proto.ConfigDescriptor{
				NginxId:  nginxID,
				SystemId: systemID,
			},
			Zconfig: &proto.ZippedFile{
				Contents:      []uint8{31, 139, 8, 0, 0, 0, 0, 0, 0, 255, 1, 0, 0, 255, 255, 0, 0, 0, 0, 0, 0, 0, 0},
				Checksum:      "30e6fa98fb48c2b132824d1ac5e2243c0be9e9082ff32598d34d7687ca7f6c7f",
				RootDirectory: "/tmp/testdata/nginx",
			},
			Zaux:         nil,
			AccessLogs:   &proto.AccessLogs{AccessLog: make([]*proto.AccessLog, 0)},
			ErrorLogs:    &proto.ErrorLogs{ErrorLog: make([]*proto.ErrorLog, 0)},
			Ssl:          &proto.SslCertificates{SslCerts: make([]*proto.SslCertificate, 0)},
			DirectoryMap: &proto.DirectoryMap{Directories: make([]*proto.Directory, 0)},
		}

		nginxConfig, err = AddAuxfileToNginxConfig(test.allowDir, nginxConfig, test.fileName, allowedDirs, true)
		assert.NoError(t, err)

		if test.expected.Zaux != nil {
			assert.Equal(t, test.expected.Zaux.Checksum, nginxConfig.GetZaux().GetChecksum())
			zf, err := zip.NewReader(nginxConfig.Zaux)
			assert.NoError(t, err)
			files := make(map[string]struct{})
			zf.RangeFileReaders(func(err error, path string, mode os.FileMode, r io.Reader) bool {
				files[path] = struct{}{}
				return true
			})
			assert.Equal(t, test.expectedAuxFiles, files)
		}

		assert.Equal(t, len(test.expected.DirectoryMap.Directories), len(nginxConfig.DirectoryMap.Directories))
		for dirIndex, expectedDirectory := range test.expected.DirectoryMap.Directories {
			resultDir := nginxConfig.DirectoryMap.Directories[dirIndex]
			assert.Equal(t, expectedDirectory.Name, resultDir.Name)
			assert.Equal(t, expectedDirectory.Permissions, resultDir.Permissions)

			assert.Equal(t, len(expectedDirectory.Files), len(resultDir.Files))
			for fileIndex, expectedFile := range expectedDirectory.Files {
				resultFile := resultDir.Files[fileIndex]
				assert.Equal(t, expectedFile.Name, resultFile.Name)
				assert.Equal(t, expectedFile.Permissions, resultFile.Permissions)
			}
		}
	}
}

func TestGetAppProtectPolicyAndSecurityLogFiles(t *testing.T) {
	testCases := []struct {
		testName    string
		file        string
		config      string
		expPolicies []string
		expProfiles []string
	}{
		{
			testName: "NoNAPContent",
			file:     "/tmp/testdata/nginx/nginx.conf",
			config: `daemon            off;
			worker_processes  2;
			user              www-data;
			
			events {
				use           epoll;
				worker_connections  128;
			}
			
			error_log         /tmp/testdata/logs/error.log info;
						
			http {
				log_format upstream_time '$remote_addr - $remote_user [$time_local] '
				'"$request" $status $body_bytes_sent '
				'"$http_referer" "$http_user_agent" '
				'rt=$request_time uct="$upstream_connect_time" uht="$upstream_header_time" urt="$upstream_response_time"';
			
				server_tokens off;
				charset       utf-8;
				
				access_log    /tmp/testdata/logs/access1.log  $upstream_time;
			
				server {
					server_name   localhost;
					listen        127.0.0.1:80;
				
					error_page    500 502 503 504  /50x.html;
					# ssl_certificate /usr/local/nginx/conf/cert.pem;
			
					location      / {
						root      /tmp/testdata/root;
					}
		
					location /privateapi {
						limit_except GET {
							auth_basic "NGINX Plus API";
							auth_basic_user_file /path/to/passwd/file;
						}
						api write=on;
						allow 127.0.0.1;
						deny  all;
					}	
				}
			
				access_log    /tmp/testdata/logs/access2.log  combined;
			
			}`,
			expPolicies: []string{},
			expProfiles: []string{},
		},
		{
			testName: "ConfigWithNAPContent",
			file:     "/tmp/testdata/nginx/nginx2.conf",
			config: `daemon            off;
			worker_processes  2;
			user              www-data;
			
			events {
				use           epoll;
				worker_connections  128;
			}
			
			error_log         /tmp/testdata/logs/error.log info;
						
			http {
				app_protect_enable on;
				app_protect_security_log_enable on;
		
				log_format upstream_time '$remote_addr - $remote_user [$time_local] '
				'"$request" $status $body_bytes_sent '
				'"$http_referer" "$http_user_agent" '
				'rt=$request_time uct="$upstream_connect_time" uht="$upstream_header_time" urt="$upstream_response_time"';
			
				server_tokens off;
				charset       utf-8;
				
				access_log    /tmp/testdata/logs/access1.log  $upstream_time;
				app_protect_policy_file /tmp/testdata/root/my-nap-policy1.json;
				app_protect_security_log "/tmp/testdata/root/log-all.json" /var/log/ssecurity.log;
			
				server {
					server_name   localhost;
					listen        127.0.0.1:80;
					app_protect_policy_file /tmp/testdata/root/my-nap-policy2.json;
					app_protect_security_log "/tmp/testdata/root/log-blocked.json" /var/log/ssecurity.log;
				
					error_page    500 502 503 504  /50x.html;
					# ssl_certificate /usr/local/nginx/conf/cert.pem;
			
					location / {
						root      /tmp/testdata/root;
						app_protect_policy_file /tmp/testdata/root/my-nap-policy3.json;
						app_protect_security_log "/tmp/testdata/root/log-default.json" /var/log/security.log;
					}
		
					location /home {
						app_protect_policy_file /tmp/testdata/root/my-nap-policy4.json;
						app_protect_security_log "/tmp/testdata/root/log-illegal.json" /var/log/security.log;
					}
		
					location /privateapi {
						app_protect_policy_file /tmp/testdata/root/my-nap-policy4.json;
						app_protect_security_log "/tmp/testdata/root/log-illegal.json" /var/log/security.log;
						limit_except GET {
							auth_basic "NGINX Plus API";
							auth_basic_user_file /path/to/passwd/file;
						}
						api write=on;
						allow 127.0.0.1;
						deny  all;
					}	
				}
			
				access_log    /tmp/testdata/logs/access2.log  combined;
			
			}`,
			expPolicies: []string{"my-nap-policy2.json", "my-nap-policy1.json", "my-nap-policy3.json", "my-nap-policy4.json"},
			expProfiles: []string{"log-all.json", "log-blocked.json", "log-default.json", "log-illegal.json"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.testName, func(t *testing.T) {
			defer tearDownDirectories()

			err := setUpFile(tc.file, []byte(tc.config))
			assert.NoError(t, err)

			allowedDirs := map[string]struct{}{}

			cfg, err := GetNginxConfig(tc.file, nginxID, systemID, allowedDirs)
			assert.NoError(t, err)

			policies, profiles := GetAppProtectPolicyAndSecurityLogFiles(cfg)
			assert.ElementsMatch(t, tc.expPolicies, policies)
			assert.ElementsMatch(t, tc.expProfiles, profiles)
		})
	}
}
