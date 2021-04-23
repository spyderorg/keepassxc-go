package keepassxc

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/kevinburke/nacl"
	"github.com/kevinburke/nacl/box"
	"github.com/kevinburke/nacl/scalarmult"
	"github.com/sirupsen/logrus"
)

const APPLICATIONNAME = "golang-keepassxc"

var ErrInvalidPeerKey = errors.New("invalid peer key")

type Client struct {
	socketPath      string
	applicationName string
	socket          *net.UnixConn
	logger          *logrus.Logger
	log             *logrus.Entry

	privateKey nacl.Key
	publicKey  nacl.Key
	peerKey    nacl.Key

	id string

	associatedName string
	associatedKey  nacl.Key
}

type ClientOption func(*Client)

func WithApplicationName(name string) ClientOption {
	return func(client *Client) {
		client.applicationName = name
	}
}

func WithLogger(logger *logrus.Logger) ClientOption {
	return func(client *Client) {
		client.logger = logger
	}
}

func NewClient(socketPath, assoName string, assoKey nacl.Key, options ...ClientOption) *Client {
	if len(assoKey) == 0 {
		assoKey = nacl.NewKey()
	}

	client := &Client{
		socketPath:      socketPath,
		applicationName: APPLICATIONNAME,
		logger:          logrus.New(),

		privateKey: nacl.NewKey(),

		associatedName: assoName,
		associatedKey:  assoKey,
	}
	client.publicKey = scalarmult.Base(client.privateKey)
	client.logger.SetLevel(logrus.PanicLevel)

	for _, option := range options {
		option(client)
	}

	client.id = client.applicationName + NaclNonceToB64(nacl.NewNonce())

	client.log = client.logger.WithFields(logrus.Fields{
		"application-name": client.applicationName,
	})

	return client
}

func (c *Client) encryptMessage(msg Message) ([]byte, error) {
	if len(c.peerKey) == 0 {
		return nil, ErrInvalidPeerKey
	}
	msgData, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return box.EasySeal(msgData, c.peerKey, c.privateKey), nil
}

func (c *Client) decryptResponse(encryptedMsg []byte) ([]byte, error) {
	if len(c.peerKey) == 0 {
		return nil, ErrInvalidPeerKey
	}
	return box.EasyOpen(encryptedMsg, c.peerKey, c.privateKey)
}

func (c *Client) sendMessage(msg Message, encrypted bool) (Response, error) {
	if encrypted {
		encryptedMsg, err := c.encryptMessage(msg)
		if err != nil {
			return nil, err
		}
		action := msg["action"]
		msg = Message{
			"action":  action,
			"message": base64.StdEncoding.EncodeToString(encryptedMsg[nacl.NonceSize:]),
			"nonce":   base64.StdEncoding.EncodeToString(encryptedMsg[:nacl.NonceSize]),
		}
	} else {
		msg["nonce"] = NaclNonceToB64(nacl.NewNonce())
	}
	msg["clientID"] = c.id

	fmt.Printf("request: %#v\n", msg)
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	_, err = c.socket.Write(data)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, 4096)
	count, err := c.socket.Read(buf)
	if err != nil {
		return nil, err
	}
	buf = buf[0:count]

	var resp Response
	err = json.Unmarshal(buf, &resp)
	if err != nil {
		return nil, err
	}

	if err, ok := resp["error"]; ok {
		return nil, errors.New(err.(string))
	}

	fmt.Printf("response before: %#v\n", resp)
	if encrypted {
		decoded, err := base64.StdEncoding.DecodeString(resp["nonce"].(string) + resp["message"].(string))
		if err != nil {
			return nil, err
		}
		decryptedMsg, err := c.decryptResponse(decoded)
		if err != nil {
			return nil, err
		}
		var msg map[string]interface{}
		err = json.Unmarshal(decryptedMsg, &msg)
		if err != nil {
			return nil, err
		}
		resp["message"] = msg
	}
	fmt.Printf("response: %#v\n", resp)

	return resp, err
}

func (c *Client) Connect() error {
	if c.socketPath == "" {
		return errors.New("unspecified socket path")
	}

	var err error
	c.socket, err = net.DialUnix("unix", nil, &net.UnixAddr{Name: c.socketPath, Net: "unix"})
	return err
}

func (c *Client) Disconnect() error {
	if c.socket != nil {
		return c.socket.Close()
	}
	return nil
}

func (c *Client) ChangePublicKeys() error {
	resp, err := c.sendMessage(Message{
		"action":    "change-public-keys",
		"publicKey": NaclKeyToB64(c.publicKey),
	}, false)
	if err != nil {
		return err
	}
	if peerKey, ok := resp["publicKey"]; ok {
		c.peerKey = B64ToNaclKey(peerKey.(string))
		return nil
	}
	return errors.New("change-public-keys failed")
}

func (c *Client) GetDatabaseHash() (string, error) {
	return "", nil
}

func (c *Client) Associate() error {
	resp, err := c.sendMessage(Message{
		"action": "associate",
		"key":    NaclKeyToB64(c.publicKey),
		"idKey":  c.associatedKey,
	}, true)
	if err != nil {
		return err
	}
	if v, ok := resp["message"]; ok {
		if msg, ok := v.(map[string]interface{}); ok {
			if id, ok := msg["id"]; ok {
				c.associatedName = id.(string)
				return nil
			}
		}
	}
	return errors.New("associate failed")
}

func (c *Client) TestAssociate() error {
	_, err := c.sendMessage(Message{
		"action": "test-associate",
		"key":    c.associatedKey,
		"id":     c.associatedName,
	}, true)
	if err != nil {
		return err
	}
	// evaluate response
	return errors.New("test-associate failed")
}

func (c *Client) CreatePassword() (*Entry, error) {
	return nil, nil
}

func (c *Client) GetLogins() ([]*Entry, error) {
	return nil, nil
}

func (c *Client) SetLogin() error {
	return nil
}

func (c *Client) LockDatabase() error {
	return nil
}

func (c *Client) GetDatabaseGroups() ([]*DBGroup, error) {
	return nil, nil
}

func (c *Client) CreateDatabaseGroup(name string) (string, string, error) {
	return "", "", nil
}

func (c *Client) GetTOTP(uuid string) (string, error) {
	return "", nil
}
