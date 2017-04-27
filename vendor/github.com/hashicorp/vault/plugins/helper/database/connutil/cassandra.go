package connutil

import (
	"crypto/tls"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mitchellh/mapstructure"

	"github.com/gocql/gocql"
	"github.com/hashicorp/vault/helper/certutil"
	"github.com/hashicorp/vault/helper/tlsutil"
)

// CassandraConnectionProducer implements ConnectionProducer and provides an
// interface for cassandra databases to make connections.
type CassandraConnectionProducer struct {
	Hosts           string `json:"hosts" structs:"hosts" mapstructure:"hosts"`
	Username        string `json:"username" structs:"username" mapstructure:"username"`
	Password        string `json:"password" structs:"password" mapstructure:"password"`
	TLS             bool   `json:"tls" structs:"tls" mapstructure:"tls"`
	InsecureTLS     bool   `json:"insecure_tls" structs:"insecure_tls" mapstructure:"insecure_tls"`
	Certificate     string `json:"certificate" structs:"certificate" mapstructure:"certificate"`
	PrivateKey      string `json:"private_key" structs:"private_key" mapstructure:"private_key"`
	IssuingCA       string `json:"issuing_ca" structs:"issuing_ca" mapstructure:"issuing_ca"`
	ProtocolVersion int    `json:"protocol_version" structs:"protocol_version" mapstructure:"protocol_version"`
	ConnectTimeout  int    `json:"connect_timeout" structs:"connect_timeout" mapstructure:"connect_timeout"`
	TLSMinVersion   string `json:"tls_min_version" structs:"tls_min_version" mapstructure:"tls_min_version"`
	Consistency     string `json:"consistency" structs:"consistency" mapstructure:"consistency"`

	Initialized bool
	Type        string
	session     *gocql.Session
	sync.Mutex
}

func (c *CassandraConnectionProducer) Initialize(conf map[string]interface{}, verifyConnection bool) error {
	c.Lock()
	defer c.Unlock()

	err := mapstructure.Decode(conf, c)
	if err != nil {
		return err
	}
	c.Initialized = true

	if verifyConnection {
		if _, err := c.Connection(); err != nil {
			return fmt.Errorf("error Initalizing Connection: %s", err)
		}
	}
	return nil
}

func (c *CassandraConnectionProducer) Connection() (interface{}, error) {
	if !c.Initialized {
		return nil, errNotInitialized
	}

	// If we already have a DB, return it
	if c.session != nil {
		return c.session, nil
	}

	session, err := c.createSession()
	if err != nil {
		return nil, err
	}

	//  Store the session in backend for reuse
	c.session = session

	return session, nil
}

func (c *CassandraConnectionProducer) Close() error {
	// Grab the write lock
	c.Lock()
	defer c.Unlock()

	if c.session != nil {
		c.session.Close()
	}

	c.session = nil

	return nil
}

func (c *CassandraConnectionProducer) createSession() (*gocql.Session, error) {
	clusterConfig := gocql.NewCluster(strings.Split(c.Hosts, ",")...)
	clusterConfig.Authenticator = gocql.PasswordAuthenticator{
		Username: c.Username,
		Password: c.Password,
	}

	clusterConfig.ProtoVersion = c.ProtocolVersion
	if clusterConfig.ProtoVersion == 0 {
		clusterConfig.ProtoVersion = 2
	}

	clusterConfig.Timeout = time.Duration(c.ConnectTimeout) * time.Second

	if c.TLS {
		var tlsConfig *tls.Config
		if len(c.Certificate) > 0 || len(c.IssuingCA) > 0 {
			if len(c.Certificate) > 0 && len(c.PrivateKey) == 0 {
				return nil, fmt.Errorf("found certificate for TLS authentication but no private key")
			}

			certBundle := &certutil.CertBundle{}
			if len(c.Certificate) > 0 {
				certBundle.Certificate = c.Certificate
				certBundle.PrivateKey = c.PrivateKey
			}
			if len(c.IssuingCA) > 0 {
				certBundle.IssuingCA = c.IssuingCA
			}

			parsedCertBundle, err := certBundle.ToParsedCertBundle()
			if err != nil {
				return nil, fmt.Errorf("failed to parse certificate bundle: %s", err)
			}

			tlsConfig, err = parsedCertBundle.GetTLSConfig(certutil.TLSClient)
			if err != nil || tlsConfig == nil {
				return nil, fmt.Errorf("failed to get TLS configuration: tlsConfig:%#v err:%v", tlsConfig, err)
			}
			tlsConfig.InsecureSkipVerify = c.InsecureTLS

			if c.TLSMinVersion != "" {
				var ok bool
				tlsConfig.MinVersion, ok = tlsutil.TLSLookup[c.TLSMinVersion]
				if !ok {
					return nil, fmt.Errorf("invalid 'tls_min_version' in config")
				}
			} else {
				// MinVersion was not being set earlier. Reset it to
				// zero to gracefully handle upgrades.
				tlsConfig.MinVersion = 0
			}
		}

		clusterConfig.SslOpts = &gocql.SslOptions{
			Config: tlsConfig,
		}
	}

	session, err := clusterConfig.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("error creating session: %s", err)
	}

	// Set consistency
	if c.Consistency != "" {
		consistencyValue, err := gocql.ParseConsistencyWrapper(c.Consistency)
		if err != nil {
			return nil, err
		}

		session.SetConsistency(consistencyValue)
	}

	// Verify the info
	err = session.Query(`LIST USERS`).Exec()
	if err != nil {
		return nil, fmt.Errorf("error validating connection info: %s", err)
	}

	return session, nil
}
