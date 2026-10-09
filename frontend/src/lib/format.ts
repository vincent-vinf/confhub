import { bytes } from './config'
import type { Format } from './types'

export async function validateContent(
  format: Format,
  content: string,
): Promise<string | undefined> {
  if (bytes(content) > 1048576) return '单份配置内容不能超过 1 MiB。'
  try {
    switch (format) {
      case 'json':
        JSON.parse(content)
        break
      case 'yaml': {
        const { parseAllDocuments } = await import('yaml')
        const documents = parseAllDocuments(content)
        const error = documents.flatMap((d) => d.errors)[0]
        if (error) throw error
        break
      }
      case 'toml': {
        const { parse } = await import('smol-toml')
        parse(content)
        break
      }
      case 'xml': {
        const document = new DOMParser().parseFromString(content, 'application/xml')
        if (document.querySelector('parsererror')) throw new Error('XML 标签或文档结构不完整。')
        break
      }
      case 'properties': {
        for (let i = 0; i < content.length; i++) {
          if (content[i] !== '\\') continue
          if (content[++i] === 'u' && !/^[0-9a-fA-F]{4}$/.test(content.slice(i + 1, i + 5)))
            throw new Error('Unicode 转义应为 \\u 后的四位十六进制数字。')
        }
        break
      }
      case 'ini': {
        for (const line of content.split('\n')) {
          const value = line.trim()
          if (!value || value.startsWith(';') || value.startsWith('#')) continue
          if (value.startsWith('[') && !/^\[[^\]]+\](?:\s*[;#].*)?$/.test(value))
            throw new Error('INI 分组应使用完整的 [分组名]。')
        }
        break
      }
    }
  } catch (error) {
    return `${format.toUpperCase()} 语法错误：${error instanceof Error ? error.message : '请检查内容。'}`
  }
}
export async function formatContent(format: Format, content: string) {
  if (format === 'json') return `${JSON.stringify(JSON.parse(content), null, 2)}\n`
  if (format === 'yaml') {
    const { parseAllDocuments } = await import('yaml')
    const documents = parseAllDocuments(content)
    const error = documents.flatMap((d) => d.errors)[0]
    if (error) throw error
    return documents.map((d) => d.toString()).join('')
  }
  return content
}
