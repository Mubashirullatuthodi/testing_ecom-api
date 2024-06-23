package controllers

import (
	"fmt"
	"math/rand"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	controllers "github.com/mubashir/e-commerce/controllers/Admin"
	"github.com/mubashir/e-commerce/initializers"
	"github.com/mubashir/e-commerce/models"
)

func PlaceOrder(ctx *gin.Context) {
	var checkout struct {
		Address_id   uint   `json:"address_id"`
		Payment_type string `json:"payment_type"`
		CouponCode   string `json:"coupon_code"`
	}
	if err := ctx.ShouldBind(&checkout); err != nil {
		ctx.JSON(400, gin.H{
			"error": "Failed to bind",
		})
		return
	}

	userid := ctx.GetUint("userid")
	var cart []models.Cart

	initializers.DB.Preload("Product").Where("user_id", userid).Find(&cart)

	//appending the amount of each product with the multiple of quantity
	var Total []int
	var Quantity int
	var discount float64

	for _, v := range cart {
		discount = controllers.OfferCalc(v.Product_ID)
		quantityPrice := (float64(v.Quantity) * v.Product.Price) - (float64(v.Quantity) * discount)
		Quantity += int(v.Quantity)
		Total = append(Total, int(quantityPrice))
	}

	//total of carts amount
	sum := 0
	for _, v := range Total {
		sum += v
	}

	fmt.Println("total=====================", Total)

	//checking coupon
	useridforcoupon := ctx.GetUint("userid")
	var couponcheck models.Coupons
	totalWithoutDiscount := sum
	//coupondiscount global
	var coupDisc float64 = 0

	if checkout.CouponCode != "" {
		//find coupon as valid or not
		if err := initializers.DB.Where("coupon_code=?", checkout.CouponCode).First(&couponcheck).Error; err != nil {

			fmt.Println("coupon code-------------->", couponcheck.CouponCode)
			ctx.JSON(401, gin.H{
				"Error": "Invalid Coupon",
			})
			return
		}
		//find the total above the condition
		if totalWithoutDiscount < couponcheck.Condition {
			sum = totalWithoutDiscount
			ctx.JSON(401, gin.H{
				"Error":                "Total amount is below 8000. Coupon cannot be used.",
				"TotalWithoutDiscount": totalWithoutDiscount,
			})
			return
		}

		//check the coupon in the database
		var usageCount int64
		initializers.DB.Model(&models.CouponUsage{}).Where("user_id=? AND coupon_id=?", useridforcoupon, couponcheck.ID).Count(&usageCount)
		if usageCount > 0 {
			ctx.JSON(401, gin.H{
				"Error": "You have already used this coupon",
			})
			return
		}

		//log coupon usage
		CouponUsage := models.CouponUsage{
			UserID:   useridforcoupon,
			CouponID: couponcheck.ID,
		}
		initializers.DB.Create(&CouponUsage)

		fmt.Println("before minus discount-------------------->", sum)
		sum -= int(couponcheck.Discount)
		coupDisc = couponcheck.Discount
		fmt.Println("after minus discount------------------>", sum)
	}

	//adrress checking
	var adrress models.Address
	if err := initializers.DB.Where("user_id = ? AND id = ?", userid, checkout.Address_id).First(&adrress).Error; err != nil {
		ctx.JSON(401, gin.H{
			"error": "Address not found",
		})
		return
	}

	orderCode := GenerateOrderID(10)

	//transaction
	tx := initializers.DB.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	//method checking
	if checkout.Payment_type == "COD" {
		if sum > 1000 {
			ctx.JSON(401, gin.H{
				"Error": "COD not available above 1000 rs",
			})
			return
		}
	}

	//payment gateway
	fmt.Println("orderid------------------->", orderCode, "grand total------------->", sum)
	if checkout.Payment_type == "UPI" {
		orderPaymentID, err := PaymentSubmission(orderCode, sum)
		if err != nil {
			ctx.JSON(401, gin.H{
				"error": err,
			})
			tx.Rollback()
			return
		}
		ctx.JSON(200, gin.H{
			"message":   "Continue to payment",
			"paymentID": orderPaymentID,
			"status":    200,
		})
		fmt.Println("paymentid-------------------->", orderPaymentID)
		fmt.Println("receipt-------------------->", orderCode)
		if err := tx.Create(&models.Payment{
			OrdID:         orderPaymentID,
			Receipt:       orderCode,
			PaymentStatus: "not done",
			PaymentAmount: int(sum),
		}); err.Error != nil {
			ctx.JSON(401, gin.H{
				"Error": "Failed to upload payment",
			})
			fmt.Println("failed to upload payment details: ", err.Error)
			tx.Rollback()
		}
	}
	//order tables
	order := models.Order{
		OrderCode:      orderCode,
		UserId:         userid,
		CouponCode:     checkout.CouponCode,
		PaymentMethod:  checkout.Payment_type,
		AddressID:      checkout.Address_id,
		TotalQuantity:  Quantity,
		OrderAmount:    float64(sum),
		CouponDiscount: int(coupDisc),
	}

	if err := tx.Create(&order); err.Error != nil {
		tx.Rollback()
		ctx.JSON(401, gin.H{
			"error": "Failed to place order",
		})
		return
	}

	for _, v := range cart {
		orderitems := models.OrderItems{
			OrderID:         order.ID,
			ProductID:       v.Product_ID,
			Quantity:        int(v.Quantity),
			SubTotal:        v.Product.Price * float64(v.Quantity),
			OfferPercentage: int(discount),
			//CouponDiscount:  int(coupDisc),
		}
		if err := tx.Create(&orderitems); err.Error != nil {
			tx.Rollback()
			ctx.JSON(401, gin.H{
				"error": "Failed place order",
			})
			fmt.Println("failed to place order items: ", err.Error)
			return
		}

		//stck managing
		convert, _ := strconv.ParseUint(v.Product.Quantity, 10, 32)
		convert -= uint64(v.Quantity)
		v.Product.Quantity = fmt.Sprint(convert)
		if err := initializers.DB.Save(&v.Product); err.Error != nil {
			ctx.JSON(401, gin.H{
				"error": "failed to update product stock",
			})
		}
	}

	if err := initializers.DB.Where("user_id=?", userid).Delete(&models.Cart{}); err.Error != nil {
		ctx.JSON(401, gin.H{
			"errror": "Failed to delete order",
		})
		return
	}

	if err := tx.Commit(); err.Error != nil {
		tx.Rollback()
		ctx.JSON(401, gin.H{
			"error": "Failed to commit transaction",
		})
		return
	}
	if checkout.Payment_type != "UPI" {
		ctx.JSON(200, gin.H{
			"message":     "Order placed successfully",
			"Grand total": sum,
			"status":      200,
		})
	}

}

const charset = "123456789ASDQWEZXC"

func GenerateOrderID(length int) string {
	rand.Seed(time.Now().UnixNano())
	orderID := "ORD_ID"

	for i := 0; i < length; i++ {
		orderID += string(charset[rand.Intn(len(charset))])
	}
	return orderID
}
