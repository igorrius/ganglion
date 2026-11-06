package agent

import "github.com/igorrius/ganglion/texts"

// The English texts the older tests compare against.
var (
	replyNoModel           = texts.EnglishText(texts.ReplyNoModel)
	replyStepLimit         = texts.EnglishText(texts.ReplyStepLimit)
	replyExpired           = texts.EnglishText(texts.ReplyExpired)
	replyDailyLimit        = texts.EnglishText(texts.ReplyDailyLimit)
	replyGiveUp            = texts.EnglishText(texts.ReplyGiveUp)
	replyNewSession        = texts.EnglishText(texts.ReplyNewSession)
	replyNoTasks           = texts.EnglishText(texts.ReplyNoTasks)
	replyTasksNotAvailable = texts.EnglishText(texts.ReplyNotAvailable)
)
