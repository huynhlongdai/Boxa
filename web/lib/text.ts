/** Chuẩn hoá để tìm kiếm không dấu: bỏ dấu tiếng Việt, đ→d, chữ thường. CJK giữ nguyên. */
export function strip(s: string) {
  return s.normalize("NFD").replace(/[\u0300-\u036f]/g, "").replace(/đ/g, "d").replace(/Đ/g, "d").toLowerCase().trim(); // i18n-ignore
}
