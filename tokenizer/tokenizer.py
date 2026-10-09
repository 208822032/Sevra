from nuxt import route, logger, Request
from nuxt.repositorys.validation import fields, use_args
import traceback
import math
import tiktoken

encoding_cache = {}


@route("/tokenizer/<str:model_name>", methods=["POST"])
@use_args({
    "role": fields.Str(required=True),
    "content": fields.Str(required=True),
    "name": fields.Str(required=False)
}, location="json")
def get_num_tokens(req: Request, message: dict, model_name: str):
    try:
        return {
            "code": 200,
            "num_tokens": num_tokens_from_messages([message], model=model_name)
        }
    except Exception as e:
        logger.error(traceback.format_exc())
        return {
            "code": 500,
            "msg": "{}".format(e)
        }


def num_tokens_from_messages(messages, model="gpt-3.5-turbo"):
    """Returns the number of tokens used by a list of messages."""
    encoding = None
    if model in encoding_cache:
        encoding = encoding_cache.get(model)
    else:
        try:
            encoding = tiktoken.encoding_for_model(model)
        except KeyError:
            logger.warning("unknown tokenizer model %s; using local estimate", model)
            encoding = None
        encoding_cache[model] = encoding
    if encoding is None:
        return estimate_tokens(messages)

    num_tokens = 0
    for message in messages:
        num_tokens += 4  # every message follows <im_start>{role/name}\n{content}<im_end>\n
        for key, value in message.items():
            if not isinstance(value, str):
                value = str(value)
            num_tokens += len(encoding.encode(value))
            if key == "name":  # if there's a name, the role is omitted
                num_tokens += -1  # role is always required and always 1 token
    num_tokens += 3  # every reply is primed with <im_start>assistant
    return num_tokens


def estimate_tokens(messages):
    """A network-free fallback for context-budget estimation."""
    num_tokens = 3
    for message in messages:
        num_tokens += 4
        for key, value in message.items():
            text = value if isinstance(value, str) else str(value)
            num_tokens += max(1, math.ceil(len(text) / 4))
            if key == "name":
                num_tokens -= 1
    return num_tokens
