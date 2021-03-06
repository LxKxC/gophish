package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/textproto"
	"time"

	"github.com/gophish/gomail"
	"github.com/jordan-wright/email"
	"gopkg.in/check.v1"
)

func (s *ModelsSuite) TestGetQueuedMailLogs(ch *check.C) {
	campaign := s.createCampaign(ch)
	ms, err := GetQueuedMailLogs(campaign.LaunchDate)
	ch.Assert(err, check.Equals, nil)
	got := make(map[string]*MailLog)
	for _, m := range ms {
		got[m.RId] = m
	}
	for _, r := range campaign.Results {
		if m, ok := got[r.RId]; ok {
			ch.Assert(m.RId, check.Equals, r.RId)
			ch.Assert(m.CampaignId, check.Equals, campaign.Id)
			ch.Assert(m.SendDate, check.Equals, campaign.LaunchDate)
			ch.Assert(m.UserId, check.Equals, campaign.UserId)
			ch.Assert(m.SendAttempt, check.Equals, 0)
		} else {
			ch.Fatalf("Result not found in maillogs: %s", r.RId)
		}
	}
}

func (s *ModelsSuite) TestMailLogBackoff(ch *check.C) {
	campaign := s.createCampaign(ch)
	result := campaign.Results[0]
	m := &MailLog{}
	err := db.Where("r_id=? AND campaign_id=?", result.RId, campaign.Id).
		Find(m).Error
	ch.Assert(err, check.Equals, nil)
	ch.Assert(m.SendAttempt, check.Equals, 0)
	ch.Assert(m.SendDate, check.Equals, campaign.LaunchDate)

	expectedError := &textproto.Error{
		Code: 500,
		Msg:  "Recipient not found",
	}
	for i := m.SendAttempt; i < MaxSendAttempts; i++ {
		err = m.Lock()
		ch.Assert(err, check.Equals, nil)
		ch.Assert(m.Processing, check.Equals, true)

		expectedDuration := math.Pow(2, float64(m.SendAttempt+1))
		expectedSendDate := m.SendDate.Add(time.Minute * time.Duration(expectedDuration))
		err = m.Backoff(expectedError)
		ch.Assert(err, check.Equals, nil)
		ch.Assert(m.SendDate, check.Equals, expectedSendDate)
		ch.Assert(m.Processing, check.Equals, false)
		result, err := GetResult(m.RId)
		ch.Assert(err, check.Equals, nil)
		ch.Assert(result.SendDate, check.Equals, expectedSendDate)
		ch.Assert(result.Status, check.Equals, STATUS_RETRY)
	}
	// Get our updated campaign and check for the added event
	campaign, err = GetCampaign(campaign.Id, int64(1))
	ch.Assert(err, check.Equals, nil)

	// We expect MaxSendAttempts + the initial campaign created event
	ch.Assert(len(campaign.Events), check.Equals, MaxSendAttempts+1)

	// Check that we receive our error after meeting the maximum send attempts
	err = m.Backoff(expectedError)
	ch.Assert(err, check.Equals, ErrMaxSendAttempts)
}

func (s *ModelsSuite) TestMailLogError(ch *check.C) {
	campaign := s.createCampaign(ch)
	result := campaign.Results[0]
	m := &MailLog{}
	err := db.Where("r_id=? AND campaign_id=?", result.RId, campaign.Id).
		Find(m).Error
	ch.Assert(err, check.Equals, nil)
	ch.Assert(m.RId, check.Equals, result.RId)

	expectedError := &textproto.Error{
		Code: 500,
		Msg:  "Recipient not found",
	}
	err = m.Error(expectedError)
	ch.Assert(err, check.Equals, nil)

	// Get our result and make sure the status is set correctly
	result, err = GetResult(result.RId)
	ch.Assert(err, check.Equals, nil)
	ch.Assert(result.Status, check.Equals, ERROR)

	// Get our updated campaign and check for the added event
	campaign, err = GetCampaign(campaign.Id, int64(1))
	ch.Assert(err, check.Equals, nil)

	expectedEventLength := 2
	ch.Assert(len(campaign.Events), check.Equals, expectedEventLength)

	gotEvent := campaign.Events[1]
	es := struct {
		Error string `json:"error"`
	}{
		Error: expectedError.Error(),
	}
	ej, _ := json.Marshal(es)
	expectedEvent := Event{
		Id:         gotEvent.Id,
		Email:      result.Email,
		Message:    EVENT_SENDING_ERROR,
		CampaignId: campaign.Id,
		Details:    string(ej),
		Time:       gotEvent.Time,
	}
	ch.Assert(gotEvent, check.DeepEquals, expectedEvent)

	ms, err := GetMailLogsByCampaign(campaign.Id)
	ch.Assert(err, check.Equals, nil)
	ch.Assert(len(ms), check.Equals, len(campaign.Results)-1)
}

func (s *ModelsSuite) TestMailLogSuccess(ch *check.C) {
	campaign := s.createCampaign(ch)
	result := campaign.Results[0]
	m := &MailLog{}
	err := db.Where("r_id=? AND campaign_id=?", result.RId, campaign.Id).
		Find(m).Error
	ch.Assert(err, check.Equals, nil)
	ch.Assert(m.RId, check.Equals, result.RId)

	err = m.Success()
	ch.Assert(err, check.Equals, nil)

	// Get our result and make sure the status is set correctly
	result, err = GetResult(result.RId)
	ch.Assert(err, check.Equals, nil)
	ch.Assert(result.Status, check.Equals, EVENT_SENT)

	// Get our updated campaign and check for the added event
	campaign, err = GetCampaign(campaign.Id, int64(1))
	ch.Assert(err, check.Equals, nil)

	expectedEventLength := 2
	ch.Assert(len(campaign.Events), check.Equals, expectedEventLength)

	gotEvent := campaign.Events[1]
	expectedEvent := Event{
		Id:         gotEvent.Id,
		Email:      result.Email,
		Message:    EVENT_SENT,
		CampaignId: campaign.Id,
		Time:       gotEvent.Time,
	}
	ch.Assert(gotEvent, check.DeepEquals, expectedEvent)

	ms, err := GetMailLogsByCampaign(campaign.Id)
	ch.Assert(err, check.Equals, nil)
	ch.Assert(len(ms), check.Equals, len(campaign.Results)-1)
}

func (s *ModelsSuite) TestGenerateMailLog(ch *check.C) {
	campaign := Campaign{
		Id:         1,
		UserId:     1,
		LaunchDate: time.Now().UTC(),
	}
	result := Result{
		RId: "abc1234",
	}
	err := GenerateMailLog(&campaign, &result)
	ch.Assert(err, check.Equals, nil)

	m := MailLog{}
	err = db.Where("r_id=?", result.RId).Find(&m).Error
	ch.Assert(err, check.Equals, nil)
	ch.Assert(m.RId, check.Equals, result.RId)
	ch.Assert(m.CampaignId, check.Equals, campaign.Id)
	ch.Assert(m.SendDate, check.Equals, campaign.LaunchDate)
	ch.Assert(m.UserId, check.Equals, campaign.UserId)
	ch.Assert(m.SendAttempt, check.Equals, 0)
	ch.Assert(m.Processing, check.Equals, false)
}

func (s *ModelsSuite) TestMailLogGenerate(ch *check.C) {
	campaign := s.createCampaign(ch)
	result := campaign.Results[0]
	m := &MailLog{}
	err := db.Where("r_id=? AND campaign_id=?", result.RId, campaign.Id).
		Find(m).Error
	ch.Assert(err, check.Equals, nil)

	msg := gomail.NewMessage()
	err = m.Generate(msg)
	ch.Assert(err, check.Equals, nil)

	expected := &email.Email{
		Subject: fmt.Sprintf("%s - Subject", result.RId),
		Text:    []byte(fmt.Sprintf("%s - Text", result.RId)),
		HTML:    []byte(fmt.Sprintf("%s - HTML", result.RId)),
	}

	msgBuff := &bytes.Buffer{}
	_, err = msg.WriteTo(msgBuff)
	ch.Assert(err, check.Equals, nil)

	got, err := email.NewEmailFromReader(msgBuff)
	ch.Assert(err, check.Equals, nil)
	ch.Assert(got.Subject, check.Equals, expected.Subject)
	ch.Assert(string(got.Text), check.Equals, string(expected.Text))
	ch.Assert(string(got.HTML), check.Equals, string(expected.HTML))
}

func (s *ModelsSuite) TestUnlockAllMailLogs(ch *check.C) {
	campaign := s.createCampaign(ch)
	ms, err := GetMailLogsByCampaign(campaign.Id)
	ch.Assert(err, check.Equals, nil)
	for _, m := range ms {
		ch.Assert(m.Processing, check.Equals, false)
	}
	err = LockMailLogs(ms, true)
	ms, err = GetMailLogsByCampaign(campaign.Id)
	ch.Assert(err, check.Equals, nil)
	for _, m := range ms {
		ch.Assert(m.Processing, check.Equals, true)
	}
	err = UnlockAllMailLogs()
	ch.Assert(err, check.Equals, nil)
	ms, err = GetMailLogsByCampaign(campaign.Id)
	ch.Assert(err, check.Equals, nil)
	for _, m := range ms {
		ch.Assert(m.Processing, check.Equals, false)
	}
}

func (s *ModelsSuite) TestURLTemplateRendering(ch *check.C) {
	template := Template{
		Name: "URLTemplate",
		UserId: 1,
		Text: "{{.URL}}",
		HTML: "{{.URL}}",
		Subject: "{{.URL}}",
	}
	ch.Assert(PostTemplate(&template), check.Equals, nil)
	campaign := s.createCampaignDependencies(ch)
	campaign.URL = "http://127.0.0.1/{{.Email}}/"
	campaign.Template = template

	ch.Assert(PostCampaign(&campaign, campaign.UserId), check.Equals, nil)
	result := campaign.Results[0]
	expectedURL := fmt.Sprintf("http://127.0.0.1/%s/?rid=%s", result.Email, result.RId)

	m := &MailLog{}
	err := db.Where("r_id=? AND campaign_id=?", result.RId, campaign.Id).
		Find(m).Error
	ch.Assert(err, check.Equals, nil)

	msg := gomail.NewMessage()
	err = m.Generate(msg)
	ch.Assert(err, check.Equals, nil)

	msgBuff := &bytes.Buffer{}
	_, err = msg.WriteTo(msgBuff)
	ch.Assert(err, check.Equals, nil)

	got, err := email.NewEmailFromReader(msgBuff)
	ch.Assert(err, check.Equals, nil)
	ch.Assert(got.Subject, check.Equals, expectedURL)
	ch.Assert(string(got.Text), check.Equals, expectedURL)
	ch.Assert(string(got.HTML), check.Equals, expectedURL)
}