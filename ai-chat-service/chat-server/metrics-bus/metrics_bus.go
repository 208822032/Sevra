package metrics_bus

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type BusMetrics struct {
	QuestionsTotalCounter                   prometheus.Counter
	KeywordsQuestionsTotalCounter           prometheus.Counter
	SensitiveQuestionsTotalCounter          prometheus.Counter
	ErrQuestionsTotalCounter                prometheus.Counter
	KvliteReadLatencyHistogram              prometheus.Histogram
	KvliteWriteLatencyHistogram             prometheus.Histogram
	KvliteTimeoutTotalCounter               prometheus.Counter
	KvliteFallbackTotalCounter              prometheus.Counter
	KvliteConnErrorTotalCounter             prometheus.Counter
	KvliteMetaRepairTotalCounter            prometheus.Counter
	KvliteShadowReadTotalCounter            prometheus.Counter
	KvliteCompareMismatchTotalCounter       prometheus.Counter
	KvliteCompareRepairTotalCounter         prometheus.Counter
	FastPathHitTotalCounter                 prometheus.Counter
	FastPathMissTotalCounter                prometheus.Counter
	SummaryBypassedTotalCounter             prometheus.Counter
	MetaCacheHitTotalCounter                prometheus.Counter
	MetaCacheMissTotalCounter               prometheus.Counter
	MetaCacheRefreshTotalCounter            prometheus.Counter
	MetaCacheStaleTotalCounter              prometheus.Counter
	RecentWindowCacheHitTotalCounter        prometheus.Counter
	RecentWindowCacheMissTotalCounter       prometheus.Counter
	RecentWindowCacheRefreshTotalCounter    prometheus.Counter
	RecentWindowCacheStaleTotalCounter      prometheus.Counter
	LoadSessionMetaLatencyHistogram         prometheus.Histogram
	LoadRecentTurnsLatencyHistogram         prometheus.Histogram
	HotSessionReadLatencyHistogram          prometheus.Histogram
	HotSessionWriteThenReadLatencyHistogram prometheus.Histogram
	SummaryBuildLatencyHistogram            prometheus.Histogram
	SummaryBuildFailTotalCounter            prometheus.Counter
	SummaryHitTotalCounter                  prometheus.Counter
	SummaryMissTotalCounter                 prometheus.Counter
	SummaryStaleTotalCounter                prometheus.Counter
	SummaryTriggerTotalCounter              prometheus.Counter
	SummaryPromptTokensBeforeHistogram      prometheus.Histogram
	SummaryPromptTokensAfterHistogram       prometheus.Histogram
	SummarySavedTokensHistogram             prometheus.Histogram
}

const (
	NAMESPACE = "ai_chat"
	SUBSYSTEM = "chat_service"
)

func NewBusMetrics(registry *prometheus.Registry) *BusMetrics {
	questionsTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "questions_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录用户提交问题的总数，仅包含记录到DB的问题数量",
	})
	keywordsQuestionsTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "keywords_questions_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录用户提交的包含关键词的问题总数",
	})
	sensitiveQuestionsTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "sensitive_questions_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录用户提交的触发敏感词的问题总数",
	})
	errQuestionsTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "err_questions_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录用户提交问题时报错的总数",
	})
	kvliteReadLatencyHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_read_latency_seconds",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite读请求耗时",
		Buckets:     prometheus.DefBuckets,
	})
	kvliteWriteLatencyHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_write_latency_seconds",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite写请求耗时",
		Buckets:     prometheus.DefBuckets,
	})
	kvliteTimeoutTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_timeout_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite超时次数",
	})
	kvliteFallbackTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_fallback_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite回退到Redis的次数",
	})
	kvliteConnErrorTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_connection_error_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite连接错误次数",
	})
	kvliteMetaRepairTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_meta_repair_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite meta 修复次数",
	})
	kvliteShadowReadTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_shadow_read_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite 影子读次数",
	})
	kvliteCompareMismatchTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_compare_mismatch_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite 一致性比对不一致次数",
	})
	kvliteCompareRepairTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_compare_repair_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite 一致性比对后的修复次数",
	})
	fastPathHitTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_hot_path_hit_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite 快路径命中次数",
	})
	fastPathMissTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_hot_path_miss_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite 快路径未命中次数",
	})
	summaryBypassedTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "summary_bypassed_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录summary 因 gating 或冷却被跳过的次数",
	})
	metaCacheHitTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_meta_cache_hit_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite session meta 本地缓存命中次数",
	})
	metaCacheMissTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_meta_cache_miss_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite session meta 本地缓存未命中次数",
	})
	metaCacheRefreshTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_meta_cache_refresh_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite session meta 本地缓存刷新次数",
	})
	metaCacheStaleTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_meta_cache_stale_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite session meta 本地缓存过期次数",
	})
	recentWindowCacheHitTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_recent_window_cache_hit_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite recent window 本地缓存命中次数",
	})
	recentWindowCacheMissTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_recent_window_cache_miss_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite recent window 本地缓存未命中次数",
	})
	recentWindowCacheRefreshTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_recent_window_cache_refresh_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite recent window 本地缓存刷新次数",
	})
	recentWindowCacheStaleTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_recent_window_cache_stale_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite recent window 本地缓存过期次数",
	})
	loadSessionMetaLatencyHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_load_session_meta_latency_seconds",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite session meta 加载耗时",
		Buckets:     prometheus.DefBuckets,
	})
	loadRecentTurnsLatencyHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_load_recent_turns_latency_seconds",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite recent turns 加载耗时",
		Buckets:     prometheus.DefBuckets,
	})
	hotSessionReadLatencyHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_hot_session_read_latency_seconds",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite 热点会话读路径耗时",
		Buckets:     prometheus.DefBuckets,
	})
	hotSessionWriteThenReadLatencyHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "kvlite_hot_session_write_then_read_latency_seconds",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录KVLite 热点会话写后读耗时",
		Buckets:     prometheus.DefBuckets,
	})
	summaryBuildLatencyHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "summary_build_latency_seconds",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录summary构建耗时",
		Buckets:     prometheus.DefBuckets,
	})
	summaryBuildFailTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "summary_build_fail_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录summary构建失败次数",
	})
	summaryHitTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "summary_hit_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录prompt重建命中summary的次数",
	})
	summaryMissTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "summary_miss_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录prompt重建未命中summary的次数",
	})
	summaryStaleTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "summary_stale_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录summary过旧但仍被使用或触发更新的次数",
	})
	summaryTriggerTotalCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "summary_trigger_total",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录summary刷新触发次数",
	})
	summaryPromptTokensBeforeHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "summary_prompt_tokens_before",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录summary压缩前prompt tokens",
		Buckets:     prometheus.DefBuckets,
	})
	summaryPromptTokensAfterHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "summary_prompt_tokens_after",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录summary压缩后prompt tokens",
		Buckets:     prometheus.DefBuckets,
	})
	summarySavedTokensHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace:   NAMESPACE,
		Subsystem:   SUBSYSTEM,
		Name:        "summary_saved_tokens",
		ConstLabels: map[string]string{"app": "ai_chat"},
		Help:        "记录summary节省的prompt tokens",
		Buckets:     prometheus.DefBuckets,
	})
	registry.MustRegister(
		questionsTotalCounter,
		keywordsQuestionsTotalCounter,
		sensitiveQuestionsTotalCounter,
		errQuestionsTotalCounter,
		kvliteReadLatencyHistogram,
		kvliteWriteLatencyHistogram,
		kvliteTimeoutTotalCounter,
		kvliteFallbackTotalCounter,
		kvliteConnErrorTotalCounter,
		kvliteMetaRepairTotalCounter,
		kvliteShadowReadTotalCounter,
		kvliteCompareMismatchTotalCounter,
		kvliteCompareRepairTotalCounter,
		fastPathHitTotalCounter,
		fastPathMissTotalCounter,
		summaryBypassedTotalCounter,
		metaCacheHitTotalCounter,
		metaCacheMissTotalCounter,
		metaCacheRefreshTotalCounter,
		metaCacheStaleTotalCounter,
		recentWindowCacheHitTotalCounter,
		recentWindowCacheMissTotalCounter,
		recentWindowCacheRefreshTotalCounter,
		recentWindowCacheStaleTotalCounter,
		loadSessionMetaLatencyHistogram,
		loadRecentTurnsLatencyHistogram,
		hotSessionReadLatencyHistogram,
		hotSessionWriteThenReadLatencyHistogram,
		summaryBuildLatencyHistogram,
		summaryBuildFailTotalCounter,
		summaryHitTotalCounter,
		summaryMissTotalCounter,
		summaryStaleTotalCounter,
		summaryTriggerTotalCounter,
		summaryPromptTokensBeforeHistogram,
		summaryPromptTokensAfterHistogram,
		summarySavedTokensHistogram,
	)
	return &BusMetrics{
		QuestionsTotalCounter:                   questionsTotalCounter,
		KeywordsQuestionsTotalCounter:           keywordsQuestionsTotalCounter,
		SensitiveQuestionsTotalCounter:          sensitiveQuestionsTotalCounter,
		ErrQuestionsTotalCounter:                errQuestionsTotalCounter,
		KvliteReadLatencyHistogram:              kvliteReadLatencyHistogram,
		KvliteWriteLatencyHistogram:             kvliteWriteLatencyHistogram,
		KvliteTimeoutTotalCounter:               kvliteTimeoutTotalCounter,
		KvliteFallbackTotalCounter:              kvliteFallbackTotalCounter,
		KvliteConnErrorTotalCounter:             kvliteConnErrorTotalCounter,
		KvliteMetaRepairTotalCounter:            kvliteMetaRepairTotalCounter,
		KvliteShadowReadTotalCounter:            kvliteShadowReadTotalCounter,
		KvliteCompareMismatchTotalCounter:       kvliteCompareMismatchTotalCounter,
		KvliteCompareRepairTotalCounter:         kvliteCompareRepairTotalCounter,
		FastPathHitTotalCounter:                 fastPathHitTotalCounter,
		FastPathMissTotalCounter:                fastPathMissTotalCounter,
		SummaryBypassedTotalCounter:             summaryBypassedTotalCounter,
		MetaCacheHitTotalCounter:                metaCacheHitTotalCounter,
		MetaCacheMissTotalCounter:               metaCacheMissTotalCounter,
		MetaCacheRefreshTotalCounter:            metaCacheRefreshTotalCounter,
		MetaCacheStaleTotalCounter:              metaCacheStaleTotalCounter,
		RecentWindowCacheHitTotalCounter:        recentWindowCacheHitTotalCounter,
		RecentWindowCacheMissTotalCounter:       recentWindowCacheMissTotalCounter,
		RecentWindowCacheRefreshTotalCounter:    recentWindowCacheRefreshTotalCounter,
		RecentWindowCacheStaleTotalCounter:      recentWindowCacheStaleTotalCounter,
		LoadSessionMetaLatencyHistogram:         loadSessionMetaLatencyHistogram,
		LoadRecentTurnsLatencyHistogram:         loadRecentTurnsLatencyHistogram,
		HotSessionReadLatencyHistogram:          hotSessionReadLatencyHistogram,
		HotSessionWriteThenReadLatencyHistogram: hotSessionWriteThenReadLatencyHistogram,
		SummaryBuildLatencyHistogram:            summaryBuildLatencyHistogram,
		SummaryBuildFailTotalCounter:            summaryBuildFailTotalCounter,
		SummaryHitTotalCounter:                  summaryHitTotalCounter,
		SummaryMissTotalCounter:                 summaryMissTotalCounter,
		SummaryStaleTotalCounter:                summaryStaleTotalCounter,
		SummaryTriggerTotalCounter:              summaryTriggerTotalCounter,
		SummaryPromptTokensBeforeHistogram:      summaryPromptTokensBeforeHistogram,
		SummaryPromptTokensAfterHistogram:       summaryPromptTokensAfterHistogram,
		SummarySavedTokensHistogram:             summarySavedTokensHistogram,
	}
}

func (m *BusMetrics) ObserveKvliteReadLatency(duration time.Duration) {
	if m != nil && m.KvliteReadLatencyHistogram != nil {
		m.KvliteReadLatencyHistogram.Observe(duration.Seconds())
	}
}

func (m *BusMetrics) ObserveKvliteWriteLatency(duration time.Duration) {
	if m != nil && m.KvliteWriteLatencyHistogram != nil {
		m.KvliteWriteLatencyHistogram.Observe(duration.Seconds())
	}
}

func (m *BusMetrics) ObserveLoadSessionMetaLatency(duration time.Duration) {
	if m != nil && m.LoadSessionMetaLatencyHistogram != nil {
		m.LoadSessionMetaLatencyHistogram.Observe(duration.Seconds())
	}
}

func (m *BusMetrics) ObserveLoadRecentTurnsLatency(duration time.Duration) {
	if m != nil && m.LoadRecentTurnsLatencyHistogram != nil {
		m.LoadRecentTurnsLatencyHistogram.Observe(duration.Seconds())
	}
}

func (m *BusMetrics) ObserveHotSessionReadLatency(duration time.Duration) {
	if m != nil && m.HotSessionReadLatencyHistogram != nil {
		m.HotSessionReadLatencyHistogram.Observe(duration.Seconds())
	}
}

func (m *BusMetrics) ObserveHotSessionWriteThenReadLatency(duration time.Duration) {
	if m != nil && m.HotSessionWriteThenReadLatencyHistogram != nil {
		m.HotSessionWriteThenReadLatencyHistogram.Observe(duration.Seconds())
	}
}

func (m *BusMetrics) IncKvliteTimeoutCount() {
	if m != nil && m.KvliteTimeoutTotalCounter != nil {
		m.KvliteTimeoutTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncKvliteFallbackCount() {
	if m != nil && m.KvliteFallbackTotalCounter != nil {
		m.KvliteFallbackTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncKvliteConnectionErrorCount() {
	if m != nil && m.KvliteConnErrorTotalCounter != nil {
		m.KvliteConnErrorTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncKvliteMetaRepairCount() {
	if m != nil && m.KvliteMetaRepairTotalCounter != nil {
		m.KvliteMetaRepairTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncKvliteShadowReadCount() {
	if m != nil && m.KvliteShadowReadTotalCounter != nil {
		m.KvliteShadowReadTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncKvliteCompareMismatchCount() {
	if m != nil && m.KvliteCompareMismatchTotalCounter != nil {
		m.KvliteCompareMismatchTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncKvliteCompareRepairCount() {
	if m != nil && m.KvliteCompareRepairTotalCounter != nil {
		m.KvliteCompareRepairTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncFastPathHitCount() {
	if m != nil && m.FastPathHitTotalCounter != nil {
		m.FastPathHitTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncFastPathMissCount() {
	if m != nil && m.FastPathMissTotalCounter != nil {
		m.FastPathMissTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncSummaryBypassedCount() {
	if m != nil && m.SummaryBypassedTotalCounter != nil {
		m.SummaryBypassedTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncMetaCacheHitCount() {
	if m != nil && m.MetaCacheHitTotalCounter != nil {
		m.MetaCacheHitTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncMetaCacheMissCount() {
	if m != nil && m.MetaCacheMissTotalCounter != nil {
		m.MetaCacheMissTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncMetaCacheRefreshCount() {
	if m != nil && m.MetaCacheRefreshTotalCounter != nil {
		m.MetaCacheRefreshTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncMetaCacheStaleCount() {
	if m != nil && m.MetaCacheStaleTotalCounter != nil {
		m.MetaCacheStaleTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncRecentWindowCacheHitCount() {
	if m != nil && m.RecentWindowCacheHitTotalCounter != nil {
		m.RecentWindowCacheHitTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncRecentWindowCacheMissCount() {
	if m != nil && m.RecentWindowCacheMissTotalCounter != nil {
		m.RecentWindowCacheMissTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncRecentWindowCacheRefreshCount() {
	if m != nil && m.RecentWindowCacheRefreshTotalCounter != nil {
		m.RecentWindowCacheRefreshTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncRecentWindowCacheStaleCount() {
	if m != nil && m.RecentWindowCacheStaleTotalCounter != nil {
		m.RecentWindowCacheStaleTotalCounter.Inc()
	}
}

func (m *BusMetrics) ObserveSummaryBuildLatency(duration time.Duration) {
	if m != nil && m.SummaryBuildLatencyHistogram != nil {
		m.SummaryBuildLatencyHistogram.Observe(duration.Seconds())
	}
}

func (m *BusMetrics) IncSummaryBuildFailCount() {
	if m != nil && m.SummaryBuildFailTotalCounter != nil {
		m.SummaryBuildFailTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncSummaryHitCount() {
	if m != nil && m.SummaryHitTotalCounter != nil {
		m.SummaryHitTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncSummaryMissCount() {
	if m != nil && m.SummaryMissTotalCounter != nil {
		m.SummaryMissTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncSummaryStaleCount() {
	if m != nil && m.SummaryStaleTotalCounter != nil {
		m.SummaryStaleTotalCounter.Inc()
	}
}

func (m *BusMetrics) IncSummaryTriggerCount() {
	if m != nil && m.SummaryTriggerTotalCounter != nil {
		m.SummaryTriggerTotalCounter.Inc()
	}
}

func (m *BusMetrics) ObserveSummaryPromptTokensBefore(tokens int) {
	if m != nil && m.SummaryPromptTokensBeforeHistogram != nil {
		m.SummaryPromptTokensBeforeHistogram.Observe(float64(tokens))
	}
}

func (m *BusMetrics) ObserveSummaryPromptTokensAfter(tokens int) {
	if m != nil && m.SummaryPromptTokensAfterHistogram != nil {
		m.SummaryPromptTokensAfterHistogram.Observe(float64(tokens))
	}
}

func (m *BusMetrics) ObserveSummarySavedTokens(tokens int) {
	if m != nil && m.SummarySavedTokensHistogram != nil {
		m.SummarySavedTokensHistogram.Observe(float64(tokens))
	}
}
