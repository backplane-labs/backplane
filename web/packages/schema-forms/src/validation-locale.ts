import { formatMessage, type ValidationError } from '@gopherex/schemapb';

const russianValidation = {
  ERROR_CODE_CHOICE_NOT_ALLOWED: 'Допустимые значения: {expected}',
  ERROR_CODE_CONST_MISMATCH: 'Ожидается значение {expected}',
  ERROR_CODE_DISCRIMINATOR_MISSING: 'Поле выбора варианта {expected} должно быть непустой строкой',
  ERROR_CODE_FORMAT_MISMATCH: 'Ожидается значение в формате {expected}',
  ERROR_CODE_GTE_VIOLATED: 'Значение должно быть не меньше {expected}',
  ERROR_CODE_GT_VIOLATED: 'Значение должно быть больше {expected}',
  ERROR_CODE_IMMUTABLE_MODIFIED: 'Это значение нельзя изменить',
  ERROR_CODE_IN_FORBIDDEN_SET: 'Недопустимые значения: {expected}',
  ERROR_CODE_LEN_MISMATCH: 'Длина должна быть ровно {expected}',
  ERROR_CODE_LIST_COUNT_MISMATCH: 'Ожидается ровно {expected} элементов',
  ERROR_CODE_LTE_VIOLATED: 'Значение должно быть не больше {expected}',
  ERROR_CODE_LT_VIOLATED: 'Значение должно быть меньше {expected}',
  ERROR_CODE_MAX_ENTRIES_VIOLATED: 'Допустимо не больше {expected} записей',
  ERROR_CODE_MAX_ITEMS_VIOLATED: 'Допустимо не больше {expected} элементов',
  ERROR_CODE_MAX_LEN_VIOLATED: 'Длина должна быть не больше {expected}',
  ERROR_CODE_MAX_PROPERTIES_VIOLATED: 'Допустимо не больше {expected} свойств',
  ERROR_CODE_MIN_ENTRIES_VIOLATED: 'Нужно не меньше {expected} записей',
  ERROR_CODE_MIN_ITEMS_VIOLATED: 'Нужно не меньше {expected} элементов',
  ERROR_CODE_MIN_LEN_VIOLATED: 'Длина должна быть не меньше {expected}',
  ERROR_CODE_MIN_PROPERTIES_VIOLATED: 'Нужно не меньше {expected} свойств',
  ERROR_CODE_MULTIPLE_OF_VIOLATED: 'Значение должно быть кратно {expected}',
  ERROR_CODE_NOT_FINITE: 'Ожидается конечное число',
  ERROR_CODE_NOT_IN_ALLOWED_SET: 'Допустимые значения: {expected}',
  ERROR_CODE_NOT_NULLABLE: 'Значение null недопустимо',
  ERROR_CODE_NOT_UNIQUE: 'Повторяющиеся значения недопустимы',
  ERROR_CODE_PATTERN_MISMATCH: 'Значение должно соответствовать шаблону {expected}',
  ERROR_CODE_PREFIX_MISMATCH: 'Значение должно начинаться с {expected}',
  ERROR_CODE_REQUIRED_MISSING: 'Обязательное поле',
  ERROR_CODE_SUFFIX_MISMATCH: 'Значение должно заканчиваться на {expected}',
  ERROR_CODE_TYPE_MISMATCH: 'Ожидается тип {expected}',
  ERROR_CODE_UNKNOWN_FIELD: 'Неизвестное поле',
  ERROR_CODE_UNKNOWN_REF: 'Неизвестная ссылка $ref: {expected}',
  ERROR_CODE_UNKNOWN_VARIANT: 'Неизвестный вариант: {actual}',
  ERROR_CODE_UNSUPPORTED_FORMAT: 'Формат {expected} не поддерживается',
};

export function validationMessage(error: ValidationError, russian: boolean): string {
  return russian ? formatMessage(error, russianValidation) : error.message;
}
