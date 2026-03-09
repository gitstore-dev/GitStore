// Structured logging utilities for Admin UI

type LogLevel = 'debug' | 'info' | 'warn' | 'error';

interface LogContext {
  [key: string]: unknown;
}

class Logger {
  private level: LogLevel;

  constructor() {
    const envLevel = import.meta.env.GITSTORE_LOG_LEVEL || 'info';
    this.level = envLevel as LogLevel;
  }

  private shouldLog(level: LogLevel): boolean {
    const levels: LogLevel[] = ['debug', 'info', 'warn', 'error'];
    return levels.indexOf(level) >= levels.indexOf(this.level);
  }

  private formatLog(level: LogLevel, message: string, context?: LogContext): void {
    if (!this.shouldLog(level)) return;

    const timestamp = new Date().toISOString();
    const logEntry = {
      timestamp,
      level,
      message,
      ...context,
    };

    const output = JSON.stringify(logEntry);

    switch (level) {
      case 'debug':
        console.debug(output);
        break;
      case 'info':
        console.info(output);
        break;
      case 'warn':
        console.warn(output);
        break;
      case 'error':
        console.error(output);
        break;
    }
  }

  debug(message: string, context?: LogContext): void {
    this.formatLog('debug', message, context);
  }

  info(message: string, context?: LogContext): void {
    this.formatLog('info', message, context);
  }

  warn(message: string, context?: LogContext): void {
    this.formatLog('warn', message, context);
  }

  error(message: string, context?: LogContext): void {
    this.formatLog('error', message, context);
  }
}

export const logger = new Logger();
