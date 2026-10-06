package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type apiResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

type harness struct {
	baseURL string
	client  *http.Client
	server  *httptest.Server
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to verify")
	flag.Parse()

	checks := map[string]func() error{
		"register-device":             checkRegisterDevice,
		"open-verification-case":      checkOpenVerificationCase,
		"record-measurement":          checkRecordMeasurement,
		"reopen-verification-case":    checkReopenVerificationCase,
		"seal-verification":           checkSealVerification,
		"seal-reviewer-qualification": checkSealReviewerQualification,
	}
	run, ok := checks[*workflow]
	if !ok {
		fmt.Fprintf(flag.CommandLine.Output(), "unknown workflow %q\n", *workflow)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Printf("workflow %s: failed: %v\n", *workflow, err)
		os.Exit(1)
	}
	fmt.Printf("workflow %s: ok\n", *workflow)
}

func checkRegisterDevice() error {
	h := newHarness()
	defer h.close()

	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": " met-1001 ",
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	id, err := nestedString(response.Body, "device", "id")
	if err != nil {
		return err
	}
	tag, err := nestedString(response.Body, "device", "asset_tag")
	if err != nil {
		return err
	}
	if tag != "MET-1001" {
		return fmt.Errorf("asset tag was not normalized: %q", tag)
	}

	read, err := h.request(http.MethodGet, "/devices/"+id, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readID, err := nestedString(read.Body, "device", "id")
	if err != nil {
		return err
	}
	if readID != id {
		return fmt.Errorf("read returned device %q, expected %q", readID, id)
	}

	duplicate, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "MET-1001",
		"model":     "Other Meter",
		"room":      "Room B",
		"steward":   "Mira",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(duplicate, "duplicate_asset_tag")
}

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened case state = %q", state)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	second, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(second, "active_case_exists")
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1201")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	accepted, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(accepted.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("accepted measurement state = %q", state)
	}
	acceptedValue, err := nestedBool(accepted.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !acceptedValue {
		return errors.New("in-tolerance measurement was not accepted")
	}

	second, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	rejectDeviceID, err := h.createDevice("MET-1202")
	if err != nil {
		return err
	}
	rejectCase, err := h.openCase(rejectDeviceID)
	if err != nil {
		return err
	}
	rejectCaseID, err := nestedString(rejectCase.Body, "case", "id")
	if err != nil {
		return err
	}
	rejected, err := h.recordMeasurement(rejectCaseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedState, err := nestedString(rejected.Body, "case", "state")
	if err != nil {
		return err
	}
	if rejectedState != "recheck_required" {
		return fmt.Errorf("out-of-tolerance state = %q", rejectedState)
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("out-of-tolerance measurement was accepted")
	}
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1301")
	if err != nil {
		return err
	}
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(reopened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("reopened case state = %q", state)
	}
	measurements, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 1 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(measurements))
	}

	second, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	return nil
}

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.readyCase("MET-1401")
	if err != nil {
		return err
	}
	from, until := validityWindow(time.Now())
	qualifications := make([]string, 2)
	for index := range qualifications {
		id, err := h.createQualification(fmt.Sprintf("Reviewer-%d", index+1), "case review", from, until)
		if err != nil {
			return err
		}
		qualifications[index] = id
	}
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer":         fmt.Sprintf("Reviewer-%d", index+1),
				"summary":          "accepted measurement sealed for review",
				"qualification_id": qualifications[index],
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("sealed case state = %q", state)
	}
	sealID, err := nestedString(read.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing")
	}
	return nil
}

// checkSealReviewerQualification exercises the full link between reviewer
// qualifications and evidence seals: rejected seals leave no trace, only a
// qualification valid at the decision instant can seal, the authority basis is
// frozen into the seal, and suspension racing sealing converges to exactly one
// outcome.
func checkSealReviewerQualification() error {
	now := time.Now()

	// Missing qualification id: rejected with no seal, and the case stays
	// review_ready and records no case_sealed event.
	h := newHarness()
	defer h.close()
	caseID, err := h.readyCase("MET-1501")
	if err != nil {
		return err
	}
	missing, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "Reviewer-A",
		"summary":  "seal without qualification",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(missing, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(missing, "invalid_input"); err != nil {
		return err
	}
	if err := expectCaseUnsealed(h, caseID, 0); err != nil {
		return fmt.Errorf("rejected seal without qualification: %w", err)
	}

	// Expired qualification: rejected and leaves no trace.
	h2 := newHarness()
	defer h2.close()
	expiredCase, err := h2.readyCase("MET-1502")
	if err != nil {
		return err
	}
	expiredQual, err := h2.createQualification("Reviewer-E", "case review", now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		return err
	}
	expiredResponse, err := h2.request(http.MethodPost, "/cases/"+expiredCase+"/seal", map[string]any{
		"reviewer":         "Reviewer-E",
		"summary":          "seal with expired qualification",
		"qualification_id": expiredQual,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(expiredResponse, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(expiredResponse, "state_conflict"); err != nil {
		return err
	}
	if err := expectCaseUnsealed(h2, expiredCase, 0); err != nil {
		return fmt.Errorf("rejected seal with expired qualification: %w", err)
	}

	// Suspended qualification: rejected and leaves no trace. A second reviewer
	// with a valid qualification can then seal, and a retry by the rejected
	// reviewer reports the existing seal rather than a half-created state.
	h3 := newHarness()
	defer h3.close()
	suspendedCase, err := h3.readyCase("MET-1503")
	if err != nil {
		return err
	}
	suspendedFrom, suspendedUntil := validityWindow(now)
	suspendedQual, err := h3.createQualification("Reviewer-S", "case review", suspendedFrom, suspendedUntil)
	if err != nil {
		return err
	}
	if _, err := h3.suspendQualification(suspendedQual, "reviewer standing review"); err != nil {
		return err
	}
	suspendedResponse, err := h3.request(http.MethodPost, "/cases/"+suspendedCase+"/seal", map[string]any{
		"reviewer":         "Reviewer-S",
		"summary":          "seal with suspended qualification",
		"qualification_id": suspendedQual,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(suspendedResponse, http.StatusConflict); err != nil {
		return err
	}
	if err := expectCaseUnsealed(h3, suspendedCase, 0); err != nil {
		return fmt.Errorf("rejected seal with suspended qualification: %w", err)
	}
	validFrom, validUntil := validityWindow(now)
	validQual, err := h3.createQualification("Reviewer-V", "case review", validFrom, validUntil)
	if err != nil {
		return err
	}
	sealedResponse, err := h3.request(http.MethodPost, "/cases/"+suspendedCase+"/seal", map[string]any{
		"reviewer":         "Reviewer-V",
		"summary":          "seal with valid qualification",
		"qualification_id": validQual,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(sealedResponse, http.StatusCreated); err != nil {
		return err
	}
	retry, err := h3.request(http.MethodPost, "/cases/"+suspendedCase+"/seal", map[string]any{
		"reviewer":         "Reviewer-S",
		"summary":          "retry after rejection",
		"qualification_id": suspendedQual,
	})
	if err != nil {
		return err
	}
	if err := expectStatus(retry, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(retry, "seal_exists"); err != nil {
		return err
	}

	// A valid seal keeps the authority basis captured at decision time even
	// after the qualification is suspended afterwards.
	if _, err := h3.suspendQualification(validQual, "post-seal standing review"); err != nil {
		return err
	}
	readAfterSuspend, err := h3.request(http.MethodGet, "/cases/"+suspendedCase, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(readAfterSuspend, http.StatusOK); err != nil {
		return err
	}
	authorityReviewer, err := nestedString(readAfterSuspend.Body, "authority", "reviewer")
	if err != nil {
		return err
	}
	if authorityReviewer != "Reviewer-V" {
		return fmt.Errorf("seal authority basis reviewer = %q, expected Reviewer-V", authorityReviewer)
	}
	authorityScope, err := nestedString(readAfterSuspend.Body, "authority", "scope")
	if err != nil {
		return err
	}
	if authorityScope != "case review" {
		return fmt.Errorf("seal authority basis scope = %q", authorityScope)
	}
	sealAuthorityID, err := nestedString(readAfterSuspend.Body, "seal", "authority", "id")
	if err != nil {
		return err
	}
	if sealAuthorityID != validQual {
		return fmt.Errorf("embedded seal basis id = %q, expected %q", sealAuthorityID, validQual)
	}
	sealCount, err := countSeals(h3, suspendedCase)
	if err != nil {
		return err
	}
	if sealCount != 1 {
		return fmt.Errorf("case has %d seals after rejected then successful sealing, expected 1", sealCount)
	}

	// Suspension and sealing submitted at the same time must converge to one
	// definite outcome. Seal-first leaves a frozen basis followed by a legal
	// suspension; suspend-first rejects the seal. The impossible outcome is a
	// seal committed against a suspended qualification.
	for attempt := 0; attempt < 10; attempt++ {
		if err := func() error {
			raceH := newHarness()
			defer raceH.close()
			raceCase, err := raceH.readyCase(fmt.Sprintf("MET-16%02d", attempt+1))
			if err != nil {
				return err
			}
			raceFrom, raceUntil := validityWindow(now)
			raceQual, err := raceH.createQualification("Reviewer-R", "case review", raceFrom, raceUntil)
			if err != nil {
				return err
			}
			type raceOutcome struct {
				sealStatus  int
				pauseStatus int
				err         error
			}
			outcomes := make(chan raceOutcome, 2)
			var wait sync.WaitGroup
			wait.Add(2)
			go func() {
				defer wait.Done()
				response, err := raceH.request(http.MethodPost, "/cases/"+raceCase+"/seal", map[string]any{
					"reviewer":         "Reviewer-R",
					"summary":          "seal racing suspension",
					"qualification_id": raceQual,
				})
				if err != nil {
					outcomes <- raceOutcome{err: err}
					return
				}
				outcomes <- raceOutcome{sealStatus: response.Status}
			}()
			go func() {
				defer wait.Done()
				response, err := raceH.request(http.MethodPost, "/qualifications/"+raceQual+"/suspend", map[string]any{
					"reason": "suspension racing seal",
				})
				if err != nil {
					outcomes <- raceOutcome{err: err}
					return
				}
				outcomes <- raceOutcome{pauseStatus: response.Status}
			}()
			wait.Wait()
			close(outcomes)
			got := raceOutcome{}
			for item := range outcomes {
				if item.err != nil {
					return item.err
				}
				got.sealStatus |= item.sealStatus
				got.pauseStatus |= item.pauseStatus
			}

			// Both operations serialize under one repository lock. Two
			// orderings are legal: seal commits first while active (201) and
			// suspension still succeeds afterwards (200); or suspension commits
			// first and the seal is rejected (409). A seal must never be
			// committed against a suspended qualification.
			if got.pauseStatus != http.StatusOK {
				return fmt.Errorf("attempt %d suspension status = %d", attempt, got.pauseStatus)
			}
			read, err := raceH.request(http.MethodGet, "/cases/"+raceCase, nil)
			if err != nil {
				return err
			}
			if err := expectStatus(read, http.StatusOK); err != nil {
				return err
			}
			state, err := nestedString(read.Body, "case", "state")
			if err != nil {
				return err
			}
			live, err := raceH.request(http.MethodGet, "/qualifications/"+raceQual, nil)
			if err != nil {
				return err
			}
			if err := expectStatus(live, http.StatusOK); err != nil {
				return err
			}
			liveStatus, err := nestedString(live.Body, "qualification", "status")
			if err != nil {
				return err
			}
			switch {
			case got.sealStatus == http.StatusCreated && state == "sealed":
				basisReviewer, err := nestedString(read.Body, "seal", "authority", "reviewer")
				if err != nil {
					return err
				}
				if basisReviewer != "Reviewer-R" {
					return fmt.Errorf("attempt %d frozen basis reviewer = %q", attempt, basisReviewer)
				}
				basisID, err := nestedString(read.Body, "seal", "authority", "id")
				if err != nil {
					return err
				}
				if basisID != raceQual {
					return fmt.Errorf("attempt %d frozen basis id = %q, expected %q", attempt, basisID, raceQual)
				}
			case got.sealStatus == http.StatusConflict && state == "review_ready":
				if _, err := nestedString(read.Body, "seal", "id"); err == nil {
					return fmt.Errorf("attempt %d rejected seal left a seal behind", attempt)
				}
			default:
				return fmt.Errorf("attempt %d inconvergent outcome: seal=%d case=%s qualification=%s", attempt, got.sealStatus, state, liveStatus)
			}
			if liveStatus != "suspended" {
				return fmt.Errorf("attempt %d qualification status after race = %q, expected suspended", attempt, liveStatus)
			}
			return nil
		}(); err != nil {
			return err
		}
	}
	return nil
}

func expectCaseUnsealed(h *harness, caseID string, wantSealedEvents int) error {
	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("case state after rejected seal = %q, expected review_ready", state)
	}
	if _, err := nestedString(read.Body, "seal", "id"); err == nil {
		return fmt.Errorf("a rejected seal left a seal record behind")
	}
	if _, exists := read.Body["authority"]; exists {
		return fmt.Errorf("a rejected seal left authority basis behind")
	}
	events, err := nestedSlice(read.Body, "events")
	if err != nil {
		return err
	}
	count := 0
	for _, item := range events {
		event, ok := item.(map[string]any)
		if ok && event["kind"] == "case_sealed" {
			count++
		}
	}
	if count != wantSealedEvents {
		return fmt.Errorf("case has %d case_sealed events, expected %d", count, wantSealedEvents)
	}
	return nil
}

func countSeals(h *harness, caseID string) (int, error) {
	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return 0, err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return 0, err
	}
	if _, err := nestedString(read.Body, "seal", "id"); err != nil {
		return 0, nil
	}
	return 1, nil
}

func newHarness() *harness {
	server := httptest.NewServer(bootstrap.NewHandler())
	return &harness{
		baseURL: server.URL,
		client:  server.Client(),
		server:  server,
	}
}

func (h *harness) close() {
	h.server.Close()
}

func (h *harness) request(method, path string, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return apiResponse{}, err
	}
	result := apiResponse{Status: response.StatusCode, Raw: string(rawBytes)}
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		if err := json.Unmarshal(rawBytes, &result.Body); err != nil {
			return apiResponse{}, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return result, nil
}

func (h *harness) createDevice(tag string) (string, error) {
	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "device", "id")
}

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
}

func (h *harness) rejectedCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) readyCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func validityWindow(now time.Time) (time.Time, time.Time) {
	return now.Add(-time.Hour), now.Add(time.Hour)
}

func (h *harness) createQualification(reviewer, scope string, validFrom, validUntil time.Time) (string, error) {
	response, err := h.request(http.MethodPost, "/qualifications", map[string]any{
		"reviewer":    reviewer,
		"scope":       scope,
		"valid_from":  validFrom.UTC().Format(time.RFC3339Nano),
		"valid_until": validUntil.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "qualification", "id")
}

func (h *harness) suspendQualification(id, reason string) (apiResponse, error) {
	return h.request(http.MethodPost, "/qualifications/"+id+"/suspend", map[string]any{
		"reason": reason,
	})
}

func expectStatus(response apiResponse, expected int) error {
	if response.Status != expected {
		return fmt.Errorf("status = %d, expected %d: %s", response.Status, expected, response.Raw)
	}
	return nil
}

func expectErrorCode(response apiResponse, expected string) error {
	code, err := nestedString(response.Body, "error", "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code = %q, expected %q", code, expected)
	}
	return nil
}

func nestedString(root map[string]any, path ...string) (string, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", strings.Join(path, "."))
	}
	return text, nil
}

func nestedBool(root map[string]any, path ...any) (bool, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%v is not a boolean", path)
	}
	return flag, nil
}

func nestedSlice(root map[string]any, key string) ([]any, error) {
	value, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", key)
	}
	return items, nil
}

func hasEvent(root map[string]any, kind string) bool {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return false
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			return true
		}
	}
	return false
}

func nestedValue(root map[string]any, path ...string) (any, error) {
	parts := make([]any, 0, len(path))
	for _, part := range path {
		parts = append(parts, part)
	}
	return nestedAny(root, parts...)
}

func nestedAny(root map[string]any, path ...any) (any, error) {
	var current any = root
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s is not an object", typed)
			}
			value, ok := object[typed]
			if !ok {
				return nil, fmt.Errorf("missing %s", typed)
			}
			current = value
		case int:
			items, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d is not inside an array", typed)
			}
			if typed < 0 || typed >= len(items) {
				return nil, fmt.Errorf("index %d is out of range", typed)
			}
			current = items[typed]
		default:
			return nil, fmt.Errorf("unsupported path element %T", part)
		}
	}
	return current, nil
}
