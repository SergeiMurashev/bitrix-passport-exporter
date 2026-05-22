package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/bitrix"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/parser"
	log "github.com/sirupsen/logrus"
)

// dealIDs godoc
// @Summary      Получить ID сделок из Bitrix24
// @Description  Загружает полный список ID сделок
// @Tags         Deals
// @Produce      json
// @Security     CookieAuth
// @Security     ApiKeyAuth
// @Security     BearerAuth
// @Success      200  {object}  SwaggerDealIDsResponse
// @Failure      400  {object}  SwaggerErrorResponse
// @Failure      401  {object}  SwaggerErrorResponse
// @Failure      502  {object}  SwaggerErrorResponse
// @Router       /api/deals/ids [get]
func (h *Handler) dealIDs(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		writeAPIErrorSimple(
			w,
			http.StatusInternalServerError,
			"WEBHOOK_EMPTY",
			"Сервер не настроен: не указан webhook Bitrix24",
			"server is not configured: BITRIX_WEBHOOK_URL is empty",
		)
		return
	}

	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		writeAPIErrorSimple(
			w,
			http.StatusBadRequest,
			"WEBHOOK_INVALID",
			"Некорректный webhook Bitrix24", "invalid webhook: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	deals, err := bClient.ListDealIDs(ctx)
	if err != nil {
		log.WithError(err).Error("deal ids load failed")
		writeAPIErrorSimple(
			w,
			http.StatusBadGateway,
			"DEAL_IDS_LOAD_FAILED",
			"Не удалось загрузить ID сделок", "failed to load deal ids: "+err.Error())
		return
	}

	writeAPISuccess(
		w,
		http.StatusOK,
		"deal_ids_loaded",
		"Список ID сделок загружен",
		map[string]any{
			"count": len(deals),
			"deals": deals,
		}, nil)
}

// dealFields godoc
// @Summary      Получить поля сделок из Bitrix24
// @Description  Возвращает список всех полей сущности CRM Deal
// @Tags         Deals
// @Produce      json
// @Security     CookieAuth
// @Security     ApiKeyAuth
// @Security     BearerAuth
// @Success      200  {object}  SwaggerDealFieldsResponse
// @Failure      400  {object}  SwaggerErrorResponse
// @Failure      401  {object}  SwaggerErrorResponse
// @Failure      502  {object}  SwaggerErrorResponse
// @Router       /api/deals/fields [get]
func (h *Handler) dealFields(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		writeAPIErrorSimple(
			w,
			http.StatusInternalServerError,
			"WEBHOOK_EMPTY",
			"Сервер не настроен: не указан webhook Bitrix24",
			"server is not configured: BITRIX_WEBHOOK_URL is empty")
		return
	}
	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		writeAPIErrorSimple(
			w,
			http.StatusBadRequest,
			"WEBHOOK_INVALID",
			"Некорректный webhook Bitrix24",
			"invalid webhook: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fields, err := bClient.ListDealFields(ctx)
	if err != nil {
		log.WithError(err).Error("deal fields load failed")
		writeAPIErrorSimple(
			w,
			http.StatusBadGateway,
			"DEAL_FIELDS_LOAD_FAILED",
			"Не удалось загрузить поля сделок",
			"failed to load deal fields: "+err.Error())
		return
	}
	writeAPISuccess(
		w,
		http.StatusOK,
		"deal_fields_loaded",
		"Поля сделок загружены",
		map[string]any{
			"count":  len(fields),
			"fields": fields,
		}, nil)
}

func (h *Handler) loadProjects(ctx context.Context, r *http.Request, bClient *bitrix.Client, dealIDs []int) ([]model.ProjectRow, string, error) {
	file, fh, err := r.FormFile("file")
	if err == nil {
		defer file.Close()
		projects, parseErr := parser.ParseDealsInput(file)
		if parseErr != nil {
			return nil, "", fmt.Errorf("failed to parse input: %w", parseErr)
		}
		return projects, fh.Filename, nil
	}

	if err != nil && err != http.ErrMissingFile {
		return nil, "", fmt.Errorf("invalid file field: %w", err)
	}
	if len(dealIDs) == 0 {
		return nil, "bitrix_api", nil
	}

	projects, err := bClient.GetDealsByIDs(ctx, dealIDs)
	if err != nil {
		if bitrix.IsAuthError(err) {
			return nil, "", fmt.Errorf("bitrix webhook is invalid or expired")
		}
		return nil, "", fmt.Errorf("failed to load deals from bitrix: %w", err)
	}
	label := "bitrix_api"
	if len(dealIDs) == 1 {
		label = fmt.Sprintf("bitrix_deal_%d", dealIDs[0])
	}
	if len(dealIDs) > 1 {
		label = fmt.Sprintf("bitrix_deals_%d", len(dealIDs))
	}
	return projects, label, nil
}

func parseDealIDs(dealIDsRaw, dealIDRaw string) ([]int, error) {
	parts := make([]string, 0, 8)
	if strings.TrimSpace(dealIDsRaw) != "" {
		parts = append(parts, strings.Split(dealIDsRaw, ",")...)
	} else if strings.TrimSpace(dealIDRaw) != "" {
		parts = append(parts, strings.TrimSpace(dealIDRaw))
	}
	if len(parts) == 0 {
		return nil, nil
	}
	seen := map[int]struct{}{}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s == "" {
			continue
		}
		id, err := strconv.Atoi(s)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("deal_ids must contain positive integers, got %q", s)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func buildDownloadFilename(src string, dealIDs []int, format string) string {
	base := strings.TrimSpace(src)
	base = strings.TrimSuffix(base, ".xlsx")
	base = strings.TrimSuffix(base, ".xls")
	base = strings.TrimSuffix(base, ".docx")
	base = strings.TrimSuffix(base, ".html")
	base = sanitizeASCII(base)
	if base == "" {
		base = "bitrix_export"
	}
	if len(dealIDs) == 0 && strings.EqualFold(src, "bitrix_api") {
		base = "bitrix_all_deals"
	}
	if len(dealIDs) == 1 {
		base = fmt.Sprintf("deal_%d", dealIDs[0])
	}
	if len(dealIDs) > 1 {
		base = fmt.Sprintf("deals_%d", len(dealIDs))
	}
	ext := ".xlsx"
	if strings.EqualFold(strings.TrimSpace(format), "docx") {
		ext = ".docx"
	}
	return base + "_passport_and_tasks" + ext
}

func parseExportFormat(raw string) (string, error) {
	f := strings.ToLower(strings.TrimSpace(raw))
	if f == "" || f == "xlsx" {
		return "xlsx", nil
	}
	if f == "docx" {
		return "docx", nil
	}
	return "", fmt.Errorf("unsupported export format %q, expected xlsx or docx", raw)
}

func sanitizeASCII(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	re := regexp.MustCompile(`[^a-z0-9._-]+`)
	s = re.ReplaceAllString(s, "_")
	s = strings.Trim(s, "._-")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func collectSupportStats(projects []model.ProjectRow) (int, int) {
	dealsWithSupport := 0
	measuresTotal := 0
	for _, p := range projects {
		support := strings.TrimSpace(p.Support)
		if support == "" {
			continue
		}
		dealsWithSupport++
		measuresTotal += countSupportItems(support)
	}
	return dealsWithSupport, measuresTotal
}

func countSupportItems(s string) int {
	lines := strings.Split(s, "\n")
	count := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}
