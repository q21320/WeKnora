package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
)

// FastGPT 同步桥接服务（独立部署的 weknora-fastgpt-sync-bridge）的基础地址。
// 通过环境变量配置，避免在代码里硬编码部署地址；未配置时回退到本地默认端口。
// 在 handler 内按需读取，支持容器运行期间通过环境变量调整而无需改代码。
const (
	syncBridgeURLEnv     = "SYNC_BRIDGE_URL"
	syncBridgeDefaultURL = "http://localhost:8000"
	// 新版桥接 /api/push 传 weknora_kb_id 时为同步等待执行（拉取全部文档分片写入 FastGPT），
	// 大知识库耗时可能达数分钟，故放宽超时。
	syncBridgeTimeout = 10 * time.Minute
)

// syncBridgePushRequest 是桥接服务 /api/push 接口约定的请求体：
// 传 weknora_kb_id 触发整库同步（桥接服务自动创建/复用 FastGPT 数据集并记录映射）。
type syncBridgePushRequest struct {
	WeKnoraKBID string `json:"weknora_kb_id"`
}

// SyncToFastGPT godoc
// @Summary      同步知识库到 FastGPT
// @Description  将知识库提交给同步桥接服务（/api/push，传 weknora_kb_id 整库同步），桥接服务同步等待执行完成后返回统计结果
// @Tags         知识库
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "知识库 ID"
// @Success      200  {object}  map[string]interface{}  "同步任务已提交"
// @Failure      503  {object}  errors.AppError         "桥接服务未配置或不可达"
// @Security     Bearer
// @Router       /knowledge-bases/{id}/sync-fastgpt [post]
func (h *KnowledgeBaseHandler) SyncToFastGPT(c *gin.Context) {
	ctx := c.Request.Context()
	kbID := c.Param("id")
	if kbID == "" {
		c.Error(errors.NewBadRequestError("Knowledge base ID cannot be empty"))
		return
	}

	bridgeResp, err := callSyncBridgePush(ctx, kbID)
	if err != nil {
		// callSyncBridgePush 返回的已经是 AppError（区分不可达/上游失败）
		c.Error(err)
		return
	}

	logger.Infof(ctx, "Knowledge base sync-to-FastGPT finished, kb: %s", secutils.SanitizeForLog(kbID))
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"knowledge_base_id": kbID,
			// 桥接服务返回的 job_id/status/total_docs/success_chunks/fail_chunks 等统计信息透传给前端
			"bridge": bridgeResp,
		},
	})
}

// callSyncBridgePush 向桥接服务提交整库同步请求（同步等待执行完成），返回桥接响应体；失败时返回 AppError。
func callSyncBridgePush(ctx context.Context, kbID string) (map[string]any, *errors.AppError) {
	baseURL := os.Getenv(syncBridgeURLEnv)
	if baseURL == "" {
		baseURL = syncBridgeDefaultURL
	}

	body, err := json.Marshal(syncBridgePushRequest{WeKnoraKBID: kbID})
	if err != nil {
		return nil, errors.NewInternalServerError("failed to build sync request: " + err.Error())
	}

	reqCtx, cancel := context.WithTimeout(ctx, syncBridgeTimeout)
	defer cancel()

	url := baseURL + "/api/push"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, errors.NewInternalServerError("failed to build sync request: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Errorf(ctx, "sync bridge unreachable, url: %s, err: %v", secutils.SanitizeForLog(url), err)
		return nil, errors.NewServiceUnavailableError(
			"Sync bridge service is unreachable, please check SYNC_BRIDGE_URL and the bridge service")
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Errorf(ctx, "sync bridge rejected the push, kb: %s, status: %d, body: %s",
			secutils.SanitizeForLog(kbID), resp.StatusCode, secutils.SanitizeForLog(string(respBody)))
		return nil, errors.NewServiceUnavailableError(
			fmt.Sprintf("Sync bridge returned status %d", resp.StatusCode))
	}

	// 解析桥接返回的任务统计（job_id/status/total_docs/success_chunks/fail_chunks 等）；
	// 解析失败不视为同步失败，只透传原始文本。
	parsed := make(map[string]any)
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		parsed["raw"] = string(respBody)
	}
	return parsed, nil
}
