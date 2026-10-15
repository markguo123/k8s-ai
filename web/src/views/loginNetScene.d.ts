/**
 * 登录页 canvas 粒子网络脚本的类型声明（纯 JS；来源为未入库的前端原型草稿，见方案 §8）。
 * 返回卸载清理函数：释放事件监听、rAF 与定时器（路由离开登录页时调用）。
 */
export declare function initLoginNet(): () => void
