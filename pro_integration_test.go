//go:build integration

package gocent_test

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
)

// These tests run the Centrifugo PRO methods against a Centrifugo PRO with
// user status, user blocking, token revocation and push notifications
// configured - with Redis storage, a PostgreSQL database and the webpush
// provider in dry run - as testdata/centrifugo-pro.compose.yml sets up. Against
// Centrifugo OSS they are skipped, unless GOCENT_TEST_PRO is set: then a server
// which is not PRO fails them, so that a broken PRO setup is not mistaken for
// passing tests.
//
// The tests use Centrifugo PRO in sandbox mode, without a license key. Centrifugo
// PRO is commercial software, under the Centrifugal Labs License Agreement
// (https://centrifugal.dev/license), not under gocent's MIT license.

func proClient(t *testing.T, ab gocent.AutoBatch) *gocent.Client {
	t.Helper()
	c := realClient(t, ab)
	if _, err := c.Connections(t.Context(), gocent.ConnectionsRequest{User: "nobody"}); err != nil {
		if os.Getenv("GOCENT_TEST_PRO") != "" {
			t.Fatalf("GOCENT_TEST_PRO is set, but the server is not Centrifugo PRO: %v", err)
		}
		t.Skipf("not Centrifugo PRO: %v", err)
	}
	return c
}

// webpushToken returns a browser PushSubscription, as webpush devices are
// registered with.
func webpushToken(t *testing.T) string {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := base64.RawURLEncoding
	return `{"endpoint":"https://fcm.googleapis.com/fcm/send/` + e.EncodeToString(k.PublicKey().Bytes()[:8]) +
		`","keys":{"p256dh":"` + e.EncodeToString(k.PublicKey().Bytes()) + `","auth":"` + e.EncodeToString(make([]byte, 16)) + `"}}`
}

func TestIntegrationProUserStatus(t *testing.T) {
	c := proClient(t, gocent.AutoBatch{})
	user := uniqueChannel(t)
	if _, err := c.UpdateUserStatus(t.Context(), gocent.UpdateUserStatusRequest{Users: []string{user}, State: "away"}); err != nil {
		t.Fatal(err)
	}
	res, err := c.GetUserStatus(t.Context(), gocent.GetUserStatusRequest{Users: []string{user}})
	if err != nil || len(res.Statuses) != 1 || res.Statuses[0].User != user || res.Statuses[0].State != "away" || res.Statuses[0].Active == 0 {
		t.Fatalf("GetUserStatus: %+v, %v", res, err)
	}
	if _, err = c.DeleteUserStatus(t.Context(), gocent.DeleteUserStatusRequest{Users: []string{user}}); err != nil {
		t.Fatal(err)
	}
	res, err = c.GetUserStatus(t.Context(), gocent.GetUserStatusRequest{Users: []string{user}})
	if err != nil || len(res.Statuses) != 1 || res.Statuses[0].Active != 0 {
		t.Fatalf("GetUserStatus after delete: %+v, %v", res, err)
	}
}

func TestIntegrationProUserAndTokenControl(t *testing.T) {
	c := proClient(t, gocent.AutoBatch{})
	user := uniqueChannel(t)
	expire := time.Now().Add(time.Hour).Unix()
	if _, err := c.BlockUser(t.Context(), gocent.BlockUserRequest{User: user, ExpireAt: expire}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UnblockUser(t.Context(), gocent.UnblockUserRequest{User: user}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RevokeToken(t.Context(), gocent.RevokeTokenRequest{UID: user + "-jti", ExpireAt: expire}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InvalidateUserTokens(t.Context(), gocent.InvalidateUserTokensRequest{User: user, IssuedBefore: time.Now().Unix(), ExpireAt: expire}); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationProDevicesAndPush(t *testing.T) {
	c := proClient(t, gocent.AutoBatch{})
	user := uniqueChannel(t)
	reg, err := c.DeviceRegister(t.Context(), gocent.DeviceRegisterRequest{
		Provider: "webpush", Platform: "web", Token: webpushToken(t), User: user,
		Meta: map[string]string{"app": "gocent"}, Topics: []string{"news"},
	})
	if err != nil || reg.ID == "" {
		t.Fatalf("DeviceRegister: %+v, %v", reg, err)
	}
	byUser := gocent.DeviceFilter{Users: []string{user}}
	list, err := c.DeviceList(t.Context(), gocent.DeviceListRequest{Filter: byUser, IncludeMeta: true, IncludeTopics: true, IncludeTotalCount: true})
	if err != nil || len(list.Items) != 1 || list.TotalCount != 1 || list.Items[0].ID != reg.ID || list.Items[0].Meta["app"] != "gocent" || !slices.Contains(list.Items[0].Topics, "news") {
		t.Fatalf("DeviceList: %+v, %v", list, err)
	}

	if _, err = c.DeviceTopicUpdate(t.Context(), gocent.DeviceTopicUpdateRequest{DeviceID: reg.ID, Op: "add", Topics: []string{"sport"}}); err != nil {
		t.Fatal(err)
	}
	topics, err := c.DeviceTopicList(t.Context(), gocent.DeviceTopicListRequest{Filter: gocent.DeviceTopicFilter{DeviceIDs: []string{reg.ID}}, IncludeTotalCount: true})
	if err != nil || topics.TotalCount != 2 {
		t.Fatalf("DeviceTopicList: %+v, %v", topics, err)
	}
	if _, err = c.DeviceUpdate(t.Context(), gocent.DeviceUpdateRequest{IDs: []string{reg.ID}, MetaUpdate: gocent.DeviceMetaUpdate{Meta: map[string]string{"app": "updated"}}}); err != nil {
		t.Fatal(err)
	}
	list, err = c.DeviceList(t.Context(), gocent.DeviceListRequest{Filter: byUser, IncludeMeta: true})
	if err != nil || len(list.Items) != 1 || list.Items[0].Meta["app"] != "updated" {
		t.Fatalf("DeviceList after update: %+v, %v", list, err)
	}

	if _, err = c.UserTopicUpdate(t.Context(), gocent.UserTopicUpdateRequest{User: user, Op: "add", Topics: []string{"alerts"}}); err != nil {
		t.Fatal(err)
	}
	userTopics, err := c.UserTopicList(t.Context(), gocent.UserTopicListRequest{Filter: gocent.UserTopicFilter{Users: []string{user}}})
	if err != nil || len(userTopics.Items) != 1 || userTopics.Items[0].Topic != "alerts" {
		t.Fatalf("UserTopicList: %+v, %v", userTopics, err)
	}

	push, err := c.SendPushNotification(t.Context(), gocent.SendPushNotificationRequest{
		Recipient:    gocent.PushRecipient{Filter: byUser},
		Notification: gocent.PushNotification{Webpush: gocent.WebPushPushNotification{Payload: jsontext.Value(`{"title":"hi"}`)}},
		SendAt:       time.Now().Add(time.Hour).Unix(),
	})
	if err != nil || push.UID == "" {
		t.Fatalf("SendPushNotification: %+v, %v", push, err)
	}
	uid := push.UID
	if _, err = c.CancelPush(t.Context(), gocent.CancelPushRequest{UID: uid}); err != nil {
		t.Fatal(err)
	}
	// Push status needs push analytics in ClickHouse; without it, Centrifugo
	// says the method is not available.
	_, err = c.UpdatePushStatus(t.Context(), gocent.UpdatePushStatusRequest{AnalyticsUID: push.UID, Status: "delivered", DeviceID: reg.ID})
	if err != nil && !errors.Is(err, gocent.ErrNotAvailable) {
		t.Fatalf("UpdatePushStatus: %v", err)
	}

	if _, err = c.DeviceRemove(t.Context(), gocent.DeviceRemoveRequest{IDs: []string{reg.ID}}); err != nil {
		t.Fatal(err)
	}
	list, err = c.DeviceList(t.Context(), gocent.DeviceListRequest{Filter: byUser})
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("DeviceList after remove: %+v, %v", list, err)
	}
}

// PRO commands in a batch, explicit and automatic, get their own results.
func TestIntegrationProBatch(t *testing.T) {
	c := proClient(t, gocent.AutoBatch{})
	user := uniqueChannel(t)
	b := c.NewBatch(gocent.BatchOptions{})
	b.UpdateUserStatus(gocent.UpdateUserStatusRequest{Users: []string{user}, State: "busy"})
	status := b.GetUserStatus(gocent.GetUserStatusRequest{Users: []string{user}})
	reg := b.DeviceRegister(gocent.DeviceRegisterRequest{Provider: "webpush", Platform: "web", Token: webpushToken(t), User: user})
	conns := b.Connections(gocent.ConnectionsRequest{User: user})
	if _, err := b.Send(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st, err := status.Result(); err != nil || len(st.Statuses) != 1 || st.Statuses[0].State != "busy" {
		t.Errorf("GetUserStatus in batch: %+v, %v", st, err)
	}
	r, err := reg.Result()
	if err != nil || r.ID == "" {
		t.Fatalf("DeviceRegister in batch: %+v, %v", r, err)
	}
	if cs, err := conns.Result(); err != nil || len(cs.Connections) != 0 {
		t.Errorf("Connections in batch: %+v, %v", cs, err)
	}

	// MaxInFlight 1 queues every call made while another is in flight.
	auto := proClient(t, gocent.AutoBatch{MaxInFlight: 1})
	results := make(chan error, 20)
	for range cap(results) {
		go func() {
			res, err := auto.DeviceList(t.Context(), gocent.DeviceListRequest{Filter: gocent.DeviceFilter{Users: []string{user}}})
			if err == nil && (len(res.Items) != 1 || res.Items[0].ID != r.ID) {
				t.Errorf("DeviceList with AutoBatch: %+v", res)
			}
			results <- err
		}()
	}
	for range cap(results) {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if _, err := c.DeviceRemove(t.Context(), gocent.DeviceRemoveRequest{IDs: []string{r.ID}}); err != nil {
		t.Fatal(err)
	}
}
