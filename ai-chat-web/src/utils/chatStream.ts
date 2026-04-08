export function extractLatestStreamChunk(responseText: string): string {
  const lastIndex = responseText.lastIndexOf('\n', responseText.length - 2)
  if (lastIndex !== -1)
    return responseText.substring(lastIndex)
  return responseText
}